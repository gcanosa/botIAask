package progtodo

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *Database {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestNetworkIsolation guards the cross-network authorization bug: the same nick on two IRC
// networks must not be able to list or delete each other's TODOs.
func TestNetworkIsolation(t *testing.T) {
	d := newTestDB(t)

	idA, err := d.Add("alpha's todo", "bob", "alpha", false, "")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := d.Add("beta's todo", "bob", "beta", false, "")
	if err != nil {
		t.Fatal(err)
	}

	listA, err := d.ListByAuthor("bob", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != idA {
		t.Fatalf("ListByAuthor(bob, alpha) leaked beta's todo: %+v", listA)
	}

	// bob on beta must not be able to delete bob's todo on alpha.
	ok, err := d.DeleteByAuthor("bob", "beta", idA)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("DeleteByAuthor allowed a cross-network delete")
	}

	// bob on beta can delete his own beta todo.
	ok, err = d.DeleteByAuthor("bob", "beta", idB)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("DeleteByAuthor failed for the owning network")
	}
}

func TestBackfillLegacyNetwork(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Add("legacy todo", "carol", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.BackfillLegacyNetwork("libera"); err != nil {
		t.Fatalf("BackfillLegacyNetwork: %v", err)
	}
	list, err := d.ListByAuthor("carol", "libera")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected backfilled todo visible under libera, got %d", len(list))
	}
}
