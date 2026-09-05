package store

import (
	"context"
	"testing"
	"time"
)

func TestAccountMetadataCompatibility_PersistsAcrossAccountQueries(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()
	record, err := st.CreateAccount(ctx, &UpstreamAccountRecord{
		ProviderType: "api_key", AccountName: "metadata-compatibility",
		CredentialRaw: "fixture-credential", Enabled: true,
		StripCodexInternalMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSetting := func(want bool) {
		t.Helper()
		byID, err := st.GetAccount(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		byFingerprint, err := st.FindAccountByFingerprint(ctx, record.Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		all, err := st.ListAccounts(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		schedulable, err := st.ListSchedulableAccounts(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 1 || len(schedulable) != 1 {
			t.Fatalf("account query counts = %d, %d", len(all), len(schedulable))
		}
		for _, got := range []*UpstreamAccountRecord{byID, byFingerprint, all[0], schedulable[0]} {
			if got == nil || got.StripCodexInternalMetadata != want {
				t.Fatalf("metadata compatibility setting = %v, want %v", got, want)
			}
		}
	}
	assertSetting(true)
	record.StripCodexInternalMetadata = false
	if err := st.UpdateAccount(ctx, record); err != nil {
		t.Fatal(err)
	}
	assertSetting(false)
}

func TestAccountMetadataCompatibility_StoreAddsLegacyColumnOnce(t *testing.T) {
	st := newTestSQLiteAccountPoolStore(t)
	ctx := context.Background()
	if _, err := st.db.Exec(`ALTER TABLE upstream_accounts DROP COLUMN strip_codex_internal_metadata`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO upstream_accounts (account_name, credential_raw, fingerprint) VALUES (?, ?, ?)`, "legacy-metadata", "fixture-credential", "legacy-metadata"); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx, true)
	if err != nil || len(accounts) != 1 || accounts[0].StripCodexInternalMetadata {
		t.Fatalf("legacy account must default to false: %v", err)
	}
	accounts[0].StripCodexInternalMetadata = true
	if err := st.UpdateAccount(ctx, accounts[0]); err != nil {
		t.Fatal(err)
	}
	reopened := NewSQLiteAccountPoolStore(st.db)
	persisted, err := reopened.GetAccount(ctx, accounts[0].ID)
	if err != nil || !persisted.StripCodexInternalMetadata {
		t.Fatalf("repeated store migration reset the setting: %v", err)
	}
}
