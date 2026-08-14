package localizationcatalog

import (
	"testing"
)

func TestCatalogCoversAGenuinelyUsefulNumberOfRealStrings(t *testing.T) {
	// A real, useful catalog, not a token handful -- harvested from
	// apps/web's actual dashboard/order-ticket/options-chain/volume-
	// profile/order-flow-footprint/DOM-replay pages.
	if count := StringKeyCount(); count < 30 {
		t.Fatalf("expected at least 30 real harvested string keys, got %d", count)
	}
}

func TestEveryStringHasATranslationInEverySupportedLanguage(t *testing.T) {
	allKeys := AllStringKeysSorted()

	for _, languageCode := range SupportedLanguages {
		translations, ok := TranslationsForLanguage(languageCode)
		if !ok {
			t.Fatalf("expected TranslationsForLanguage to succeed for supported language %s", languageCode)
		}
		if len(translations) != len(allKeys) {
			t.Fatalf("language %s: expected %d translations, got %d", languageCode, len(allKeys), len(translations))
		}
		for _, key := range allKeys {
			text, exists := translations[key]
			if !exists || text == "" {
				t.Fatalf("language %s: missing or empty translation for key %q", languageCode, key)
			}
		}
	}
}

func TestEnglishIsAmongTheSupportedLanguages(t *testing.T) {
	found := false
	for _, lang := range SupportedLanguages {
		if lang == LanguageEnglish {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected English to be a supported language so clients never need a hardcoded fallback")
	}
}

func TestAtLeastTwoNonEnglishLanguagesAreSupported(t *testing.T) {
	nonEnglishCount := 0
	for _, lang := range SupportedLanguages {
		if lang != LanguageEnglish {
			nonEnglishCount++
		}
	}
	if nonEnglishCount < 2 {
		t.Fatalf("expected at least 2 non-English languages, got %d", nonEnglishCount)
	}
}

func TestUnsupportedLanguageReturnsFalse(t *testing.T) {
	if _, ok := TranslationsForLanguage("fr"); ok {
		t.Fatalf("expected an unsupported language code to return ok=false, not a silent empty map")
	}
}

func TestKnownRealStringsAreTranslatedCorrectlyIntoHindiAndTamil(t *testing.T) {
	hindi, _ := TranslationsForLanguage(LanguageHindi)
	tamil, _ := TranslationsForLanguage(LanguageTamil)

	if hindi["orderTicket.heading"] != "ऑर्डर टिकट" {
		t.Fatalf("unexpected Hindi translation for orderTicket.heading: %q", hindi["orderTicket.heading"])
	}
	if tamil["orderTicket.heading"] != "ஆர்டர் டிக்கெட்" {
		t.Fatalf("unexpected Tamil translation for orderTicket.heading: %q", tamil["orderTicket.heading"])
	}
	if hindi["dashboard.positions.empty"] != "कोई खुली पोजीशन नहीं है।" {
		t.Fatalf("unexpected Hindi translation for dashboard.positions.empty: %q", hindi["dashboard.positions.empty"])
	}
}

func TestEnglishTranslationsMatchTheRealSourceStrings(t *testing.T) {
	english, _ := TranslationsForLanguage(LanguageEnglish)

	expected := map[string]string{
		"orderTicket.heading":                    "Order ticket",
		"dashboard.positions.empty":              "No open positions.",
		"strategies.followVerified.shortHeading": "Follow strategies",
		"optionsChain.strike":                    "Strike",
	}
	for key, want := range expected {
		if got := english[key]; got != want {
			t.Fatalf("key %s: expected English %q, got %q", key, want, got)
		}
	}
}
