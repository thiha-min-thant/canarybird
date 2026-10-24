package server

import (
	"hash/fnv"
	"os"
	"strings"
)

// Identity is who this pod is. It travels on every response as headers and
// JSON so the grid can colour by version and the stats can count pods.
type Identity struct {
	Version   string `json:"version"`
	Color     string `json:"color"` // CSS colour, resolved from COLOR or derived from Version
	Pod       string `json:"pod"`
	Node      string `json:"node"`
	Zone      string `json:"zone"`
	Namespace string `json:"namespace"`
}

// Palette maps friendly names to colours that stay distinguishable on a dark
// canvas and when projected. Order matters: it is the auto-assignment order.
var Palette = []struct{ Name, Hex string }{
	{"blue", "#4C8DFF"},
	{"green", "#3DD68C"},
	{"yellow", "#F5C842"},
	{"purple", "#A97BFF"},
	{"orange", "#FF8A3D"},
	{"pink", "#FF6BB5"},
	{"teal", "#2CC9C2"},
	{"white", "#E8EDF3"},
}

func IdentityFromEnv() Identity {
	id := Identity{
		Version:   envOr("VERSION", "v1"),
		Pod:       envOr("POD_NAME", hostname()),
		Node:      os.Getenv("NODE_NAME"),
		Zone:      os.Getenv("ZONE"),
		Namespace: os.Getenv("NAMESPACE"),
	}
	id.Color = ResolveColor(os.Getenv("COLOR"), id.Version)
	return id
}

// ResolveColor accepts a palette name, any CSS colour, or "" (derive from version).
func ResolveColor(c, version string) string {
	c = strings.TrimSpace(strings.ToLower(c))
	for _, p := range Palette {
		if c == p.Name {
			return p.Hex
		}
	}
	if c != "" {
		return c
	}
	h := fnv.New32a()
	h.Write([]byte(version))
	return Palette[int(h.Sum32())%len(Palette)].Hex
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	return h
}
