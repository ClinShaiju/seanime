package main

import (
	"embed"
	"fmt"
	"os"
	"seanime/internal/constants"
	"seanime/internal/server"
)

//go:embed all:web
var WebFS embed.FS

//go:embed internal/icon/logo.png
var embeddedLogo []byte

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(constants.Version)
		os.Exit(0)
	}
	server.StartServer(WebFS, embeddedLogo)
}
