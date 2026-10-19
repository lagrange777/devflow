package assets

import "embed"

//go:embed skills/*/SKILL.md invariants.json
var Files embed.FS
