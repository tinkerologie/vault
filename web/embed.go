package web

import "embed"

// Dist embeds the built SPA files from the dist directory.
// Build the frontend first: cd web && npm run build
//
//go:embed dist/*
var Dist embed.FS
