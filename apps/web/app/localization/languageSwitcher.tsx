"use client";

// Mercurius / web — language selector, wired to backoffice's real
// internal/localizationcatalog GET /localization/languages and
// GET /localization/{lang}, via useLocalization()'s LocalizationProvider.
// Selecting a language here actually swaps visible strings on any page
// that calls translate() — see app/page.tsx's OrderTicketSection /
// dashboard headings for the proof.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useLocalization } from "./localizationContext";

const LANGUAGE_DISPLAY_NAMES_BY_CODE: Record<string, string> = {
  en: "English",
  hi: "हिन्दी (Hindi)",
  ta: "தமிழ் (Tamil)",
};

export default function LanguageSwitcher() {
  const { supportedLanguageCodes, selectedLanguageCode, setSelectedLanguageCode, isLoadingCatalog, catalogFetchErrorMessage } =
    useLocalization();

  return (
    <div className="flex flex-col gap-1 text-xs">
      <label className="flex items-center gap-2">
        <span className="text-neutral-500">Language</span>
        <select
          className="rounded border px-2 py-1 text-sm"
          value={selectedLanguageCode}
          onChange={(changeEvent) => setSelectedLanguageCode(changeEvent.target.value)}
        >
          {supportedLanguageCodes.map((languageCode) => (
            <option key={languageCode} value={languageCode}>
              {LANGUAGE_DISPLAY_NAMES_BY_CODE[languageCode] ?? languageCode}
            </option>
          ))}
        </select>
        {isLoadingCatalog && <span className="text-neutral-400">loading…</span>}
      </label>
      {catalogFetchErrorMessage && <p className="max-w-xs text-red-600">{catalogFetchErrorMessage}</p>}
    </div>
  );
}
