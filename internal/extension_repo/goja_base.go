package extension_repo

import (
	"context"
	"encoding/json"
	"fmt"
	"seanime/internal/events"
	"seanime/internal/extension"
	"seanime/internal/goja/goja_bindings"
	"seanime/internal/goja/goja_runtime"
	"seanime/internal/plugin"
	gojautil "seanime/internal/util/goja"
	"sync"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
	"github.com/rs/zerolog"
)

// bindingRegistry owns the Fetch and ChromeDP instances bound to an extension's
// runtimes. BindFetch and BindChromeDP each spawn a pump goroutine per runtime that
// only exits when its binding is closed, so every binding created for an extension
// must be closed when that extension is unloaded — including the ones minted later by
// a pool factory miss, not just the pre-warmed ones.
type bindingRegistry struct {
	mu      sync.Mutex
	closed  bool
	closers []func()
}

// add takes ownership of f.
func (r *bindingRegistry) add(f *goja_bindings.Fetch) {
	if f == nil {
		return
	}
	r.addCloser(f.Close)
}

// addChromeDP takes ownership of c, the ChromeDP binding ShareBinds installs on every
// runtime. Unlike Fetch this is bound eagerly whether or not the extension ever calls
// it, so it is the common case rather than a rare one.
func (r *bindingRegistry) addChromeDP(c *goja_bindings.ChromeDP) {
	if c == nil {
		return
	}
	r.addCloser(c.Close)
}

// addCloser registers close. If the registry is already closed (i.e. the extension was
// unloaded while a runtime was being created), close is called immediately so the
// binding's pump goroutine cannot outlive the extension.
func (r *bindingRegistry) addCloser(close func()) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		close()
		return
	}
	r.closers = append(r.closers, close)
	r.mu.Unlock()
}

func (r *bindingRegistry) closeAll() {
	r.mu.Lock()
	r.closed = true
	closers := r.closers
	r.closers = nil
	r.mu.Unlock()

	for _, close := range closers {
		close()
	}
}

type gojaProviderBase struct {
	ext            *extension.Extension
	logger         *zerolog.Logger
	pool           *goja_runtime.Pool
	program        *goja.Program
	source         string
	runtimeManager *goja_runtime.Manager
	store          *plugin.Store[string, any]
	scheduler      *gojautil.Scheduler
	bindings       bindingRegistry
	wsEventManager events.WSEventManagerInterface
}

func initializeProviderBase(
	ext *extension.Extension,
	language extension.Language,
	logger *zerolog.Logger,
	runtimeManager *goja_runtime.Manager,
	wsEventManager events.WSEventManagerInterface,
) (*gojaProviderBase, error) {
	// initFn, pr, err := SetupGojaExtensionVM(ext, language, logger)
	// if err != nil {
	// 	return nil, err
	// }
	source := ext.Payload
	if language == extension.LanguageTypescript {
		var err error
		source, err = JSVMTypescriptToJS(ext.Payload)
		if err != nil {
			logger.Error().Err(err).Str("id", ext.ID).Msg("extensions: Failed to convert typescript")
			return nil, err
		}
	}

	// Compile the program once, to be reused by all VMs
	program, err := goja.Compile("", source, false)
	if err != nil {
		logger.Error().Err(err).Str("id", ext.ID).Msg("extensions: Failed to compile program")
		return nil, fmt.Errorf("compilation failed: %w", err)
	}

	providerBase := &gojaProviderBase{
		ext:            ext,
		logger:         logger,
		pool:           nil, // to be set
		program:        program,
		source:         source,
		runtimeManager: runtimeManager,
		store:          plugin.NewStore[string, any](nil), // Create a store (must be stopped when unloading)
		scheduler:      gojautil.NewScheduler(),           // Create a scheduler (must be stopped when unloading)
		wsEventManager: wsEventManager,
	}

	initFn := func() *goja.Runtime {
		vm := goja.New()
		vm.SetParserOptions(parser.WithDisableSourceMaps)
		providerBase.store.Bind(vm, providerBase.scheduler)
		// Bind the shared bindings
		providerBase.bindings.addChromeDP(ShareBinds(vm, logger, ext, wsEventManager))
		providerBase.bindings.add(goja_bindings.BindFetch(ext.ID, vm))
		gojautil.BindMutable(vm)
		BindUserConfig(vm, ext, logger)
		return vm
	}

	pool, err := runtimeManager.GetOrCreatePrivatePool(ext.ID, initFn)
	if err != nil {
		return nil, err
	}

	providerBase.pool = pool

	return providerBase, nil
}

func (g *gojaProviderBase) GetExtension() *extension.Extension {
	return g.ext
}

func (g *gojaProviderBase) callClassMethod(ctx context.Context, methodName string, args ...interface{}) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	vm, err := g.pool.Get(ctx)
	if err != nil {
		g.logger.Error().Err(err).Str("id", g.ext.ID).Msg("extension: Failed to get VM")
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	defer func() {
		g.pool.Put(vm)
	}()

	// Ensure the Provider class is defined only once per VM
	providerType, err := vm.RunString("typeof Provider")
	if err != nil {
		g.logger.Error().Err(err).Str("id", g.ext.ID).Msg("extension: Failed to check Provider existence")
		return nil, fmt.Errorf("failed to check Provider existence: %w", err)
	}
	if providerType.String() == "undefined" {
		_, err = vm.RunProgram(g.program)
		if err != nil {
			g.logger.Error().Err(err).Str("id", g.ext.ID).Msg("extension: Failed to run program")
			return nil, fmt.Errorf("failed to run program: %w", err)
		}
	}

	// Create a new instance of the Provider class
	providerInstance, err := vm.RunString("new Provider()")
	if err != nil {
		g.logger.Error().Err(err).Str("id", g.ext.ID).Msg("extension: Failed to create Provider instance")
		return nil, fmt.Errorf("failed to create Provider instance: %w", err)
	}

	if providerInstance == nil {
		g.logger.Error().Str("id", g.ext.ID).Msg("extension: Provider constructor returned nil")
		return nil, fmt.Errorf("provider constructor returned nil")
	}

	// Get the method from the instance
	method, ok := goja.AssertFunction(providerInstance.ToObject(vm).Get(methodName))
	if !ok {
		g.logger.Error().Str("id", g.ext.ID).Str("method", methodName).Msg("extension: Method not found or not a function")
		return nil, fmt.Errorf("method %s not found or not a function", methodName)
	}

	// Convert arguments to Goja values
	gojaArgs := make([]goja.Value, len(args))
	for i, arg := range args {
		gojaArgs[i] = vm.ToValue(arg)
	}

	// Call the method
	result, err := method(providerInstance, gojaArgs...)
	if err != nil {
		g.logger.Error().Err(err).Str("id", g.ext.ID).Str("method", methodName).Msg("extension: Method execution failed")
		return nil, fmt.Errorf("method %s execution failed: %w", methodName, err)
	}

	// g.runtimeManager.PrintBasePoolMetrics()

	return g.awaitAndExportValue(ctx, result)
}

// unmarshalValue unmarshals a Goja value to a target interface
// This is used to convert the result of a method call to a struct
func (g *gojaProviderBase) unmarshalValue(value any, target interface{}) error {
	if value == nil {
		return fmt.Errorf("cannot unmarshal nil value")
	}

	exported := value
	if gojaValue, ok := value.(goja.Value); ok {
		exported = gojaValue.Export()
	}
	if exported == nil {
		return fmt.Errorf("exported value is nil")
	}

	data, err := json.Marshal(exported)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}
	return json.Unmarshal(data, target)
}

// waitForPromise waits for a promise to resolve and returns the result
func (g *gojaProviderBase) waitForPromise(value any) (any, error) {
	return g.awaitAndExportValue(context.Background(), value)
}

func (g *gojaProviderBase) awaitAndExportValue(ctx context.Context, value any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	switch v := value.(type) {
	case nil:
		return nil, nil
	case *goja.Promise:
		if err := gojautil.WaitForPromise(ctx, v); err != nil {
			return nil, err
		}

		if v.State() == goja.PromiseStateRejected {
			return nil, fmt.Errorf("promise rejected: %v", exportGojaValue(v.Result()))
		}

		return g.awaitAndExportValue(ctx, v.Result())
	case goja.Value:
		if v == nil {
			return nil, nil
		}

		if promise, ok := v.Export().(*goja.Promise); ok {
			return g.awaitAndExportValue(ctx, promise)
		}

		return exportGojaValue(v), nil
	default:
		return value, nil
	}
}

func exportGojaValue(value goja.Value) any {
	if value == nil {
		return nil
	}

	return value.Export()
}

func (g *gojaProviderBase) PutVM(vm *goja.Runtime) {
	g.pool.Put(vm)
}

func (g *gojaProviderBase) ClearInterrupt() {
	g.store.Stop()
	g.scheduler.Stop()
	// Delete the pool, like GojaPlugin does. GetOrCreatePrivatePool returns an EXISTING pool
	// as-is and discards the new initFn, so without this a reload would hand the new provider
	// a stale pool whose factory is still bound to this provider's bindingRegistry — which
	// closeAll() below marks closed forever, leaving every fetch() promise unsettled.
	if g.runtimeManager != nil {
		g.runtimeManager.DeletePluginPool(g.ext.ID)
	}
	// Terminate the fetch and chromedp pump goroutines bound to this provider's runtimes.
	// Done after DeletePluginPool so the factory can no longer mint untracked runtimes;
	// any that still slips through is closed on arrival by the registry.
	g.bindings.closeAll()
}
