import assert from "node:assert/strict"
import { existsSync } from "node:fs"
import { createRequire } from "node:module"
import { dirname, join } from "node:path"
import test from "node:test"
import { fileURLToPath } from "node:url"

const require = createRequire(import.meta.url)
const denshiRoot = join(dirname(fileURLToPath(import.meta.url)), "..")

const pkg = require("../package.json")
const builderConfig = require("../electron-builder.js")

// The 4-segment fork version (X.Y.Z.N) is stored in package.json as semver "X.Y.Z-N" and mapped
// back in two places that must not drift: electron-builder.js (artifact names) and
// src/main/version.ts (what the update prompt shows the user).
test("package.json version is a fork version", () => {
    assert.match(pkg.version, /^\d+\.\d+\.\d+-\d+$/)
})

test("electron-builder buildVersion is the 4-segment form", () => {
    const [base, revision] = pkg.version.split("-")
    assert.equal(builderConfig.buildVersion, `${base}.${revision}`)
})

test("the main process maps versions the same way", { skip: !existsSync(join(denshiRoot, "dist/main/version.js")) && "run npm run build:main first" }, () => {
    const { toDisplayVersion } = require("../dist/main/version.js")
    assert.equal(toDisplayVersion(pkg.version), builderConfig.buildVersion)
    // Upstream's plain semver passes through untouched (the "seanime" update channels serve it).
    assert.equal(toDisplayVersion("3.10.2"), "3.10.2")
})
