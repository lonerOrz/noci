package dist

import _ "embed"

//go:embed index.html
var IndexHTML string

//go:embed app.js
var AppJS string

//go:embed style.css
var StyleCSS string

//go:embed favicon.svg
var FaviconSVG []byte
