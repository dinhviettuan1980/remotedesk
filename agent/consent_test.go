package main

import "testing"

type countInjector struct{ keys, moves, text int }

func (c *countInjector) MouseMove(x, y float64)                      { c.moves++ }
func (c *countInjector) MouseButton(btn int, down bool)              {}
func (c *countInjector) Wheel(dx, dy float64)                        {}
func (c *countInjector) Key(code string, down bool)                  { c.keys++ }
func (c *countInjector) Text(s string)                               { c.text++ }
func (c *countInjector) ReleaseAll()                                 {}
func (c *countInjector) Wake()                                       {}
func (c *countInjector) SetDisplay(d Display)                        {}
func (c *countInjector) FocusedEdit() (bool, float64, float64, bool) { return false, 0, 0, false }

func TestConsentGatesInput(t *testing.T) {
	inj := &countInjector{}
	a := &Agent{cfg: &Config{Confirm: true}, input: inj}
	for _, m := range []*inMsg{{T: "kd", C: "KeyA"}, {T: "mm", X: .5, Y: .5}} {
		if !a.input1(m) {
			t.Fatalf("%s not taken as input", m.T)
		}
	}
	if inj.keys+inj.moves != 0 {
		t.Fatalf("input applied before consent: %+v", *inj)
	}
	a.approved = true
	a.input1(&inMsg{T: "kd", C: "KeyA"})
	a.input1(&inMsg{T: "mm", X: .5, Y: .5})
	if inj.keys != 1 || inj.moves != 1 {
		t.Fatalf("input not applied after consent: %+v", *inj)
	}
	a.cfg.Confirm, a.approved = false, false // no "confirm": never gated
	a.input1(&inMsg{T: "kd", C: "KeyA"})
	if inj.keys != 2 {
		t.Fatalf("input gated without confirm: %+v", *inj)
	}
}
