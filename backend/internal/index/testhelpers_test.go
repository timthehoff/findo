package index

import "testing"

// testOwner returns the id of a shared test user, creating them on first use,
// so tests that only care about volumes needn't set up accounts.
func testOwner(t testing.TB, idx *Index) int64 {
	t.Helper()
	if u, ok, err := idx.GetUserByName("owner"); err != nil {
		t.Fatal(err)
	} else if ok {
		return u.ID
	}
	u, err := idx.CreateUser("owner", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// sealed stands in for encryption: the index stores whatever bytes it's given.
func sealed(b []byte) func(int64) ([]byte, error) {
	return func(int64) ([]byte, error) { return b, nil }
}

// allVolumeIDs is a scope covering every volume, for tests that aren't about scoping.
func allVolumeIDs(t testing.TB, idx *Index) []int64 {
	t.Helper()
	vols, err := idx.ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, v := range vols {
		ids = append(ids, v.ID)
	}
	return ids
}
