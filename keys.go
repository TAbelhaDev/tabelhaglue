package main

import (
	"path/filepath"

	"github.com/charmbracelet/bubbles/key"
	"github.com/TAbelhaDev/tabelhatuiui"
)

// reg is taglue's single source of truth for keybindings: defaults
// registered below, overrides persisted to ~/.config/taglue/keybindings.json.
// Resolve() returns the effective binding, shared by dispatch, footer and
// help modal — a user rebind applies to all at once, the same pattern
// tabelharadar/tabelhakanban/tabelhajobs already use.
var reg = tuiui.NewKeyRegistry(filepath.Join(tuiui.ConfigDir(), "taglue", "keybindings.json"))

func init() {
	reg.RegisterMany(
		tuiui.Action{ID: "quit", Help: "sair", Keys: []string{"q"}},
		tuiui.Action{ID: "help", Help: "atalhos", Keys: []string{"?"}},
		tuiui.Action{ID: "run", Help: "rodar", Keys: []string{"r", "enter"}, Label: "r/enter"},
		tuiui.Action{ID: "back", Help: "voltar", Keys: []string{"esc"}},
		tuiui.Action{ID: "scroll", Help: "navegar", Keys: []string{"j", "k", "up", "down"}, Label: "j/k"},
		// Same "nav" convention as tabelharadar/tabelhajobs/tabelhanet.
		tuiui.Action{ID: "nav", Help: "move focus", Keys: []string{"ctrl+h", "ctrl+l"}, Label: "ctrl+h/l"},
		tuiui.Action{ID: "toggle-schedule", Help: "ativar/desativar agendamento", Keys: []string{"e"}},
	)
}

// resolve is a short alias so Update reads like the old named keys.
func resolve(id string) key.Binding { return reg.Resolve(id) }

// bindingsOf returns the resolved bindings for a list of action IDs — used by
// per-mode footers/help sections so they reflect rebinds live, mirroring
// tabelhajobs' helper of the same name.
func bindingsOf(ids ...string) []key.Binding {
	out := make([]key.Binding, 0, len(ids))
	for _, id := range ids {
		out = append(out, reg.Resolve(id))
	}
	return out
}
