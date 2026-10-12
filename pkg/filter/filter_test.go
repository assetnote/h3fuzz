package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewFilterByName(t *testing.T) {
	scf, _ := NewFilterByName("status", "200")
	if _, ok := scf.(*StatusFilter); !ok {
		t.Errorf("Was expecting statusfilter")
	}

	szf, _ := NewFilterByName("size", "200")
	if _, ok := szf.(*SizeFilter); !ok {
		t.Errorf("Was expecting sizefilter")
	}

	wf, _ := NewFilterByName("word", "200")
	if _, ok := wf.(*WordFilter); !ok {
		t.Errorf("Was expecting wordfilter")
	}

	lf, _ := NewFilterByName("line", "200")
	if _, ok := lf.(*LineFilter); !ok {
		t.Errorf("Was expecting linefilter")
	}

	ref, _ := NewFilterByName("regexp", "200")
	if _, ok := ref.(*RegexpFilter); !ok {
		t.Errorf("Was expecting regexpfilter")
	}

	tf, _ := NewFilterByName("time", "200")
	if _, ok := tf.(*TimeFilter); !ok {
		t.Errorf("Was expecting timefilter")
	}
}

func TestNewFilterByNameError(t *testing.T) {
	_, err := NewFilterByName("status", "invalid")
	if err == nil {
		t.Errorf("Was expecing an error")
	}
}

func TestNewFilterByNameNotFound(t *testing.T) {
	_, err := NewFilterByName("nonexistent", "invalid")
	if err == nil {
		t.Errorf("Was expecing an error with invalid filter name")
	}
}

func TestMatcherManagerMarshalJSONPreservesPublishedState(t *testing.T) {
	manager := NewMatcherManager()
	manager.SetCalibrated(true)
	if err := manager.AddMatcher("status", "200"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddFilter("size", "10", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddPerDomainFilter("example.test", "word", "2"); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(manager)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	for _, fragment := range []string{
		`"IsCalibrated":true`,
		`"Matchers":{"status":`,
		`"Filters":{"size":`,
		`"PerDomainFilters":{"example.test":`,
	} {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("serialized MatcherManager %s does not contain %s", data, fragment)
		}
	}
}
