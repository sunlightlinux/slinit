package dissect

import "testing"

func TestParsePolicyLoose(t *testing.T) {
	for _, in := range []string{"", "loose", "default", "  "} {
		p, err := ParsePolicy(in)
		if err != nil {
			t.Errorf("ParsePolicy(%q): %v", in, err)
			continue
		}
		if p.Strict {
			t.Errorf("ParsePolicy(%q): Strict should be false", in)
		}
	}
}

func TestParsePolicyStrict(t *testing.T) {
	p, err := ParsePolicy("strict")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Strict {
		t.Error("strict policy should set Strict=true")
	}
}

func TestParsePolicyFullForm(t *testing.T) {
	p, err := ParsePolicy("root=verity+encrypted+signed:usr=verity:home=encrypted")
	if err != nil {
		t.Fatal(err)
	}
	if p.Strict {
		t.Error("full-form policy should default Strict=false")
	}
	if len(p.PerPartition["root"]) != 3 {
		t.Errorf("root constraints: %v", p.PerPartition["root"])
	}
	if len(p.PerPartition["usr"]) != 1 || p.PerPartition["usr"][0] != "verity" {
		t.Errorf("usr constraints: %v", p.PerPartition["usr"])
	}
	if len(p.PerPartition["home"]) != 1 || p.PerPartition["home"][0] != "encrypted" {
		t.Errorf("home constraints: %v", p.PerPartition["home"])
	}
}

func TestParsePolicyInvalidToken(t *testing.T) {
	if _, err := ParsePolicy("bogus-token-without-equals"); err == nil {
		t.Error("expected error on token without `=`")
	}
}
