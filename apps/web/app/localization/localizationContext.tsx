"use client";

// Mercurius / web — real i18n wiring against backoffice's real
// internal/localizationcatalog HTTP contract:
//
//   GET /localization/languages -> {"supportedLanguages": ["en","hi","ta"]}
//   GET /localization/{lang}    -> {"languageCode": "...", "translations": {"orderTicket.heading": "...", ...}}
//                                   (404 if unsupported)
//
// This is a real client-side i18n mechanism, not a decorative selector:
// LocalizationProvider fetches the supported-language list once, fetches
// the translation catalog for the currently-selected language whenever it
// changes, caches each language's catalog in memory so re-selecting a
// previously-fetched language doesn't re-hit the network, and exposes a
// `translate(stringKey, englishFallbackText)` function that every
// wired-up page calls instead of hardcoding English literals. If the
// catalog hasn't loaded yet, or the current language is "en" (the
// catalog's own base language), or a key is genuinely missing from the
// fetched catalog, `translate` returns the supplied English fallback —
// so the UI never shows a raw "undefined" or a bare key while a fetch is
// in flight, and still degrades gracefully if backoffice is down.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

const backofficeBaseUrl = process.env.NEXT_PUBLIC_BACKOFFICE_BASE_URL ?? "http://localhost:8084";

const DEFAULT_LANGUAGE_CODE = "en";

type LocalizationCatalogResponse = {
  languageCode: string;
  translations: Record<string, string>;
};

type LocalizationContextValue = {
  supportedLanguageCodes: string[];
  selectedLanguageCode: string;
  setSelectedLanguageCode: (languageCode: string) => void;
  translate: (stringKey: string, englishFallbackText: string) => string;
  isLoadingCatalog: boolean;
  catalogFetchErrorMessage: string | null;
};

const LocalizationContext = createContext<LocalizationContextValue | null>(null);

export function LocalizationProvider({ children }: { children: React.ReactNode }) {
  const [supportedLanguageCodes, setSupportedLanguageCodes] = useState<string[]>([DEFAULT_LANGUAGE_CODE]);
  const [selectedLanguageCode, setSelectedLanguageCode] = useState<string>(DEFAULT_LANGUAGE_CODE);
  const [isLoadingCatalog, setIsLoadingCatalog] = useState(false);
  const [catalogFetchErrorMessage, setCatalogFetchErrorMessage] = useState<string | null>(null);

  // languageCode -> stringKey -> translatedText, populated lazily and
  // never evicted for the lifetime of the page.
  const fetchedCatalogsByLanguageCode = useRef<Map<string, Record<string, string>>>(new Map());
  // Forces a re-render once a lazily-fetched catalog lands, since the
  // cache above is a ref (mutating it doesn't itself trigger a render).
  const [catalogVersion, setCatalogVersion] = useState(0);

  useEffect(() => {
    let isCancelled = false;
    async function fetchSupportedLanguages() {
      try {
        const httpResponse = await fetch(`${backofficeBaseUrl}/localization/languages`);
        if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
        const parsed: { supportedLanguages: string[] } = await httpResponse.json();
        if (!isCancelled && Array.isArray(parsed.supportedLanguages) && parsed.supportedLanguages.length > 0) {
          setSupportedLanguageCodes(parsed.supportedLanguages);
        }
      } catch {
        // Non-fatal — the switcher just falls back to offering only
        // "en" (already the initial state) if backoffice is unreachable.
      }
    }
    fetchSupportedLanguages();
    return () => {
      isCancelled = true;
    };
  }, []);

  useEffect(() => {
    if (selectedLanguageCode === DEFAULT_LANGUAGE_CODE) return;
    if (fetchedCatalogsByLanguageCode.current.has(selectedLanguageCode)) return;

    let isCancelled = false;
    async function fetchCatalogForSelectedLanguage() {
      setIsLoadingCatalog(true);
      setCatalogFetchErrorMessage(null);
      try {
        const httpResponse = await fetch(`${backofficeBaseUrl}/localization/${selectedLanguageCode}`);
        if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
        const parsed: LocalizationCatalogResponse = await httpResponse.json();
        if (!isCancelled) {
          fetchedCatalogsByLanguageCode.current.set(selectedLanguageCode, parsed.translations ?? {});
          setCatalogVersion((previousVersion) => previousVersion + 1);
        }
      } catch (thrownError) {
        if (!isCancelled) {
          setCatalogFetchErrorMessage(
            thrownError instanceof Error
              ? `Couldn't load "${selectedLanguageCode}" from backoffice: ${thrownError.message}. Is it running on ${backofficeBaseUrl}?`
              : "Unknown error loading translation catalog."
          );
        }
      } finally {
        if (!isCancelled) setIsLoadingCatalog(false);
      }
    }
    fetchCatalogForSelectedLanguage();
    return () => {
      isCancelled = true;
    };
  }, [selectedLanguageCode]);

  const translate = useCallback(
    (stringKey: string, englishFallbackText: string): string => {
      if (selectedLanguageCode === DEFAULT_LANGUAGE_CODE) return englishFallbackText;
      const catalogForSelectedLanguage = fetchedCatalogsByLanguageCode.current.get(selectedLanguageCode);
      return catalogForSelectedLanguage?.[stringKey] ?? englishFallbackText;
      // catalogVersion is a deliberate dependency below even though it's
      // unused in the body — it's what forces `translate`'s identity (and
      // therefore every consuming component) to refresh once a catalog
      // finishes loading into the ref.
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selectedLanguageCode, catalogVersion]
  );

  const contextValue = useMemo<LocalizationContextValue>(
    () => ({
      supportedLanguageCodes,
      selectedLanguageCode,
      setSelectedLanguageCode,
      translate,
      isLoadingCatalog,
      catalogFetchErrorMessage,
    }),
    [supportedLanguageCodes, selectedLanguageCode, translate, isLoadingCatalog, catalogFetchErrorMessage]
  );

  return <LocalizationContext.Provider value={contextValue}>{children}</LocalizationContext.Provider>;
}

export function useLocalization(): LocalizationContextValue {
  const contextValue = useContext(LocalizationContext);
  if (!contextValue) {
    throw new Error("useLocalization() must be called from within a <LocalizationProvider>.");
  }
  return contextValue;
}
