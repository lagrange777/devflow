package flow

import (
	"encoding/json"
	"strings"
)

func jsonDecoder(s string) *json.Decoder { return json.NewDecoder(strings.NewReader(s)) }
