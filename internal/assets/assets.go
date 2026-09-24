// Package assets embeds the optional browser assets Gust serves from its proxy.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
	"strings"
)

//go:embed sounds/*.ogg
var soundFS embed.FS

// Sound describes an embedded audio asset served by the proxy.
type Sound struct {
	// Name is the file name without the .ogg extension.
	Name string
	// Hash is a short content hash used for cache busting.
	Hash string
	data []byte
}

var sounds = loadSounds()

func loadSounds() []Sound {
	entries, err := fs.ReadDir(soundFS, "sounds")
	if err != nil {
		return nil
	}
	out := make([]Sound, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".ogg") {
			continue
		}
		data, err := soundFS.ReadFile("sounds/" + name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		out = append(out, Sound{
			Name: strings.TrimSuffix(name, ".ogg"),
			Hash: hex.EncodeToString(sum[:8]),
			data: data,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Sounds returns the embedded audio assets sorted by name.
func Sounds() []Sound { return sounds }

// Lookup returns an embedded sound and its bytes by base name.
func Lookup(name string) (Sound, []byte, bool) {
	for _, sound := range sounds {
		if sound.Name == name {
			return sound, sound.data, true
		}
	}
	return Sound{}, nil, false
}
