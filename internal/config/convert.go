package config

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConvertReport says what Convert did to a LAN Sentinel file, for the
// operator to review before installing the result (docs/MIGRATION.md).
type ConvertReport struct {
	// Removed lists keys of functionality LAN Tracker no longer has.
	Removed []ConvertNote `json:"removed"`
	// Changed lists keys whose meaning changed: the old value is dropped
	// and the new default applies, or the value was rewritten.
	Changed []ConvertNote `json:"changed"`
	// Attention lists what the operator must review or do by hand.
	Attention []string `json:"attention"`
	// Errors is what still fails validation in the converted file.
	Errors []FieldError `json:"errors,omitempty"`
}

// ConvertNote is one key Convert removed or changed.
type ConvertNote struct {
	Key string `json:"key"`
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
	Why string `json:"why"`
}

// ErrNotLegacy means the file is not a LAN Sentinel version-1
// configuration.
var ErrNotLegacy = errors.New("not a LAN Sentinel configuration (version 1)")

// legacyPaths are LAN Sentinel's install paths and their LAN Tracker
// counterparts.
var legacyPaths = []struct{ old, new string }{
	{"/data/lan-sentinel/", "/data/lan-tracker/"},
	{"/run/lan-sentinel/", "/run/lan-tracker/"},
	{"/etc/lan-sentinel/", "/etc/lan-tracker/"},
}

// Convert turns a LAN Sentinel configuration (schema version 1) into a
// LAN Tracker one (version 2). It is a pure function of the file: it
// writes nothing, and it never enables probing. Every interface's active
// discovery is switched off in the result, since rates changed meaning
// (ADR 0012); the operator enables it again after review. Comments on
// kept keys survive.
func Convert(data []byte) ([]byte, ConvertReport, error) {
	r := ConvertReport{Removed: []ConvertNote{}, Changed: []ConvertNote{}, Attention: []string{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, r, fmt.Errorf("parse: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, r, fmt.Errorf("%w: the top level is not a mapping", ErrNotLegacy)
	}
	root := doc.Content[0]
	if v := child(root, "version"); v == nil || v.Value != "1" {
		return nil, r, ErrNotLegacy
	}
	child(root, "version").Value = "2"
	r.Changed = append(r.Changed, ConvertNote{Key: "version", Old: "1", New: "2", Why: "LAN Tracker schema"})

	remove := func(m *yaml.Node, key, prefix, why string) {
		if v := drop(m, key); v != nil {
			r.Removed = append(r.Removed, ConvertNote{Key: join(prefix, key), Old: text(v), Why: why})
		}
	}
	// reset drops a key whose value LAN Tracker reads differently, so the
	// conservative default applies.
	reset := func(m *yaml.Node, key, prefix, why string) {
		if v := drop(m, key); v != nil {
			r.Changed = append(r.Changed, ConvertNote{Key: join(prefix, key), Old: text(v), New: "default", Why: why})
		}
	}
	const (
		identification = "device identification is not part of LAN Tracker (ADR 0012)"
		rates          = "rates are probe starts per second now, not packets; the old value is not equivalent (ADR 0012)"
	)

	if ifs := child(root, "interfaces"); ifs != nil && ifs.Kind == yaml.SequenceNode {
		for i, ic := range ifs.Content {
			prefix := fmt.Sprintf("interfaces[%d]", i)
			act := child(ic, "active")
			if act == nil || act.Kind != yaml.MappingNode {
				continue
			}
			remove(act, "identify", prefix+".active", identification)
			if en := child(act, "enabled"); en != nil && en.Value == "true" {
				en.Value = "false"
				name := prefix
				if n := child(ic, "name"); n != nil {
					name = n.Value
				}
				r.Changed = append(r.Changed, ConvertNote{Key: prefix + ".active.enabled", Old: "true", New: "false",
					Why: "conversion never enables probing"})
				r.Attention = append(r.Attention, fmt.Sprintf("active discovery is off on %s: review its networks and excludes, "+
					"then set active.enabled again (deploy/config.active-example.yaml)", name))
			}
		}
	}
	if act := child(root, "active"); act != nil && act.Kind == yaml.MappingNode {
		remove(act, "udp", "active", "the NTP and EtherNet/IP probes are removed (ADR 0012)")
		remove(act, "identify", "active", identification)
		reset(act, "max_packets_per_second", "active", rates+"; the default is 5 starts/s")
		reset(act, "max_concurrent_probes", "active", "renamed max_concurrent_jobs; the default is 4")
		reset(act, "budgets", "active", rates+"; the defaults are arp 5, icmp 1 starts/s and tcp 1 connect/s, 2 per interface, 1 per host")
		reset(act, "max_sweep_targets", "active", "the default per-interface sweep cap is 1024 addresses")
		if v := child(act, "min_target_interval"); v != nil {
			var d Duration
			if d.parse(v.Value) != nil || d < Defaults().Active.MinTargetInterval {
				reset(act, "min_target_interval", "active", "the default spacing to one target is 5s")
			}
		}
		if tcp := child(act, "tcp"); tcp != nil && tcp.Kind == yaml.MappingNode && child(tcp, "targets") == nil {
			r.Attention = append(r.Attention, "active.tcp.targets is unset: the defaults are ports 80, 443, 8080 and 502")
		}
	}
	if profs := child(root, "profiles"); profs != nil && profs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(profs.Content); {
			name, p := profs.Content[i].Value, profs.Content[i+1]
			if p.Kind == yaml.MappingNode {
				remove(p, "udp", "profiles."+name, "the UDP probes are removed (ADR 0012)")
				if len(p.Content) == 0 { // it only ran UDP probes
					drop(profs, name)
					r.Removed = append(r.Removed, ConvertNote{Key: "profiles." + name, Why: "the profile ran only UDP probes"})
					continue
				}
			}
			i += 2
		}
	}
	if id := child(root, "identity"); id != nil && id.Kind == yaml.MappingNode {
		remove(id, "identifiers", "identity", identification)
		remove(id, "address_overlap", "identity", "a MAC's addresses are independent: a new one never replaces another")
	}
	for _, k := range [][2]string{{"storage", "path"}, {"api", "socket"}, {"identity", "oui_override"}} {
		m := child(root, k[0])
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		v := child(m, k[1])
		if v == nil {
			continue
		}
		for _, p := range legacyPaths {
			if strings.HasPrefix(v.Value, p.old) {
				old := v.Value
				v.Value = p.new + strings.TrimPrefix(v.Value, p.old)
				r.Changed = append(r.Changed, ConvertNote{Key: k[0] + "." + k[1], Old: old, New: v.Value, Why: "LAN Tracker path"})
			}
		}
	}
	r.Attention = append(r.Attention,
		"the database is not converted here: copy LAN Sentinel's stopped database to the new path, then start LAN Tracker on the copy (deploy/README.md)",
		"LAN_SENTINEL_* environment variables are refused; rename them to LAN_TRACKER_*")

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, r, fmt.Errorf("encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, r, fmt.Errorf("encode: %w", err)
	}
	out := buf.Bytes()
	if _, err := load(out, "converted", LoadOptions{}); err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			return out, r, err
		}
		r.Errors = ve.Errors
	}
	return out, r, nil
}

// uncommented returns a copy of n without comments.
func uncommented(n *yaml.Node) *yaml.Node {
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = uncommented(x)
	}
	return &c
}

// child returns the value of key in mapping m, or nil.
func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// drop removes key from mapping m and returns its value, or nil.
func drop(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return v
		}
	}
	return nil
}

// text renders a node on one line for the report, without its comments.
func text(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, err := yaml.Marshal(uncommented(n))
	if err != nil {
		return "…"
	}
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
