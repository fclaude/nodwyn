package symbols

import _ "embed"

//go:embed grammars/THIRD_PARTY_NOTICES.txt
var grammarNotices string

//go:embed grammars/LICENSE.wasitter
var bindingLicense string

//go:embed grammars/LICENSE.wazero
var runtimeLicense string

// Licenses keeps redistribution notices accessible in the standalone binary.
func Licenses() string {
	return "Bundled Tree-sitter support: third-party notices\n\n" + grammarNotices +
		"\n\nwasitter Go binding\n\n" + bindingLicense + "\n\nwazero runtime\n\n" + runtimeLicense
}
