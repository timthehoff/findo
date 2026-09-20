package index

import "testing"

func TestCreateAndGetVolume(t *testing.T) {
	idx := openTest(t)

	v, err := idx.CreateVolume("Home NAS", "nas.local", "media", "findo", []byte("ciphertext"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if v.ID == 0 {
		t.Fatalf("expected a non-zero id")
	}
	if !v.Enabled {
		t.Fatalf("expected a new volume to default to enabled")
	}
	if v.LastTestOK != nil {
		t.Fatalf("expected no test result yet, got %v", v.LastTestOK)
	}

	got, ok, err := idx.GetVolume(v.ID)
	if err != nil || !ok {
		t.Fatalf("GetVolume: ok=%v err=%v", ok, err)
	}
	if got.Name != "Home NAS" || got.Host != "nas.local" || got.Share != "media" || got.Username != "findo" {
		t.Fatalf("unexpected volume: %+v", got)
	}
}

func TestListVolumesOrdersByCreation(t *testing.T) {
	idx := openTest(t)

	first, err := idx.CreateVolume("First", "h1", "s1", "u1", []byte("x"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	second, err := idx.CreateVolume("Second", "h2", "s2", "u2", []byte("x"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	vols, err := idx.ListVolumes()
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 2 || vols[0].ID != first.ID || vols[1].ID != second.ID {
		t.Fatalf("unexpected order: %+v", vols)
	}
}

func TestUpdateVolumeKeepsPasswordWhenNotProvided(t *testing.T) {
	idx := openTest(t)

	v, err := idx.CreateVolume("Vol", "host", "share", "user", []byte("original"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	updated, err := idx.UpdateVolume(v.ID, VolumeUpdate{
		Name: "Renamed", Host: "host2", Share: "share2", Username: "user2", Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpdateVolume: %v", err)
	}
	if updated.Name != "Renamed" || updated.Host != "host2" {
		t.Fatalf("update not applied: %+v", updated)
	}

	secret, err := idx.GetVolumeSecret(v.ID)
	if err != nil {
		t.Fatalf("GetVolumeSecret: %v", err)
	}
	if string(secret.PasswordEnc) != "original" {
		t.Fatalf("expected password to be left alone, got %q", secret.PasswordEnc)
	}
}

func TestUpdateVolumeReplacesPasswordWhenProvided(t *testing.T) {
	idx := openTest(t)

	v, err := idx.CreateVolume("Vol", "host", "share", "user", []byte("original"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	if _, err := idx.UpdateVolume(v.ID, VolumeUpdate{
		Name: "Vol", Host: "host", Share: "share", Username: "user",
		PasswordEnc: []byte("replaced"), Enabled: true,
	}); err != nil {
		t.Fatalf("UpdateVolume: %v", err)
	}

	secret, err := idx.GetVolumeSecret(v.ID)
	if err != nil {
		t.Fatalf("GetVolumeSecret: %v", err)
	}
	if string(secret.PasswordEnc) != "replaced" {
		t.Fatalf("expected replaced password, got %q", secret.PasswordEnc)
	}
}

func TestDeleteVolumeCascadesFilesAndCrawlRuns(t *testing.T) {
	idx := openTest(t)

	v, err := idx.CreateVolume("Vol", "host", "share", "user", []byte("x"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	runID, err := idx.StartCrawlRun(v.ID, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.Upsert(File{VolumeID: v.ID, Path: "a.txt", Name: "a.txt"}, runID); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := idx.DeleteVolume(v.ID); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}

	if _, ok, err := idx.GetVolume(v.ID); err != nil || ok {
		t.Fatalf("expected volume to be gone: ok=%v err=%v", ok, err)
	}
	if _, ok, err := idx.Stat(v.ID, "a.txt"); err != nil || ok {
		t.Fatalf("expected the volume's files to be gone: ok=%v err=%v", ok, err)
	}
	if _, ok, err := idx.LatestCrawlRun(v.ID); err != nil || ok {
		t.Fatalf("expected the volume's crawl runs to be gone: ok=%v err=%v", ok, err)
	}
}

func TestSetVolumeTestResult(t *testing.T) {
	idx := openTest(t)

	v, err := idx.CreateVolume("Vol", "host", "share", "user", []byte("x"))
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	if err := idx.SetVolumeTestResult(v.ID, false, "connection refused"); err != nil {
		t.Fatalf("SetVolumeTestResult: %v", err)
	}
	got, ok, err := idx.GetVolume(v.ID)
	if err != nil || !ok {
		t.Fatalf("GetVolume: ok=%v err=%v", ok, err)
	}
	if got.LastTestOK == nil || *got.LastTestOK {
		t.Fatalf("expected a failed test result, got %+v", got.LastTestOK)
	}
	if got.LastTestError != "connection refused" {
		t.Fatalf("unexpected error message: %q", got.LastTestError)
	}

	if err := idx.SetVolumeTestResult(v.ID, true, ""); err != nil {
		t.Fatalf("SetVolumeTestResult: %v", err)
	}
	got, _, err = idx.GetVolume(v.ID)
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if got.LastTestOK == nil || !*got.LastTestOK {
		t.Fatalf("expected a successful test result, got %+v", got.LastTestOK)
	}
	if got.LastTestError != "" {
		t.Fatalf("expected error to be cleared, got %q", got.LastTestError)
	}
}
