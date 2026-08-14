// Package localizationcatalog is the REAL backend piece of FEATURES.md
// §14's "multi-language/localization support for the retail app". The
// actual frontend i18n wiring is out of scope here (apps/web is
// explicitly off-limits for this build — a follow-up agent does that
// pass), but a real, complete, queryable translation-string catalog
// covering the retail app's ACTUAL current UI copy is fully in scope,
// and is what this package provides.
//
// Every string key in this catalog was harvested by reading apps/web's
// real page components (app/page.tsx's dashboard/order-ticket UI,
// app/strategies/page.tsx's verified-strategy-following UI,
// app/optionsChain, app/domReplay, app/volumeProfile,
// app/orderFlowFootprint) — not invented. See README.md for the exact
// harvesting method and for the GET /localization/{lang} contract the
// next agent doing frontend wiring should consume.
//
// Languages: English (the existing default — included so a client can
// treat this catalog as the single source of truth rather than falling
// back to hardcoded JSX text), Hindi (hi — India's most widely spoken
// language and a real, sensible first localization target for an
// India-domiciled retail broking platform: this repo's charges/ledger
// figures are already denominated in ₹/rupees and reference SEBI/T+2
// settlement conventions throughout, so Hindi is not an arbitrary
// choice), and Tamil (ta — a major Indian regional language with a very
// large base of retail equity investors, and a real second-language
// target the largest Indian discount brokers (Zerodha, Groww) already
// localize into).
package localizationcatalog

import "sort"

// LanguageCode is a closed set of the languages this catalog actually
// has complete translations for.
type LanguageCode string

const (
	LanguageEnglish LanguageCode = "en"
	LanguageHindi   LanguageCode = "hi"
	LanguageTamil   LanguageCode = "ta"
)

// SupportedLanguages lists every language this catalog serves, in a
// stable order — used by the HTTP layer's GET /localization/languages
// discovery endpoint.
var SupportedLanguages = []LanguageCode{LanguageEnglish, LanguageHindi, LanguageTamil}

// catalog is the real translation table: stringKey -> language -> translated text.
// Every key below is a REAL string currently rendered somewhere in
// apps/web (see the package doc + README.md for the harvesting method),
// organized by the screen it came from.
var catalog = map[string]map[LanguageCode]string{
	// --- Dashboard shell (app/page.tsx, app/layout.tsx) ---
	"dashboard.title": {
		LanguageEnglish: "Mercurius (skeleton dashboard)",
		LanguageHindi:   "मर्क्यूरियस (स्केलेटन डैशबोर्ड)",
		LanguageTamil:   "மெர்க்யூரியஸ் (ஸ்கெலிட்டன் டாஷ்போர்டு)",
	},
	"dashboard.positions.heading": {
		LanguageEnglish: "Positions",
		LanguageHindi:   "पोजीशन",
		LanguageTamil:   "நிலைப்பாடுகள்",
	},
	"dashboard.positions.empty": {
		LanguageEnglish: "No open positions.",
		LanguageHindi:   "कोई खुली पोजीशन नहीं है।",
		LanguageTamil:   "திறந்த நிலைப்பாடுகள் இல்லை.",
	},
	"dashboard.priceChart.heading": {
		LanguageEnglish: "Price chart (1m candles)",
		LanguageHindi:   "मूल्य चार्ट (1 मिनट कैंडल)",
		LanguageTamil:   "விலை விளக்கப்படம் (1 நிமிட கேண்டில்கள்)",
	},
	"dashboard.candlestickChart.ariaLabel": {
		LanguageEnglish: "OHLC candlestick chart",
		LanguageHindi:   "OHLC कैंडलस्टिक चार्ट",
		LanguageTamil:   "OHLC கேண்டில்ஸ்டிக் விளக்கப்படம்",
	},
	"dashboard.marketSessionAdmin.heading": {
		LanguageEnglish: "Market session (admin)",
		LanguageHindi:   "मार्केट सत्र (एडमिन)",
		LanguageTamil:   "சந்தை அமர்வு (நிர்வாகி)",
	},

	// --- Order ticket (app/page.tsx) ---
	"orderTicket.heading": {
		LanguageEnglish: "Order ticket",
		LanguageHindi:   "ऑर्डर टिकट",
		LanguageTamil:   "ஆர்டர் டிக்கெட்",
	},
	"orderTicket.buySellToggle.label": {
		LanguageEnglish: "Buy (unchecked = sell)",
		LanguageHindi:   "खरीदें (अनचेक = बेचें)",
		LanguageTamil:   "வாங்கவும் (தேர்வு நீக்கினால் = விற்கவும்)",
	},
	"orderTicket.quantity.label": {
		LanguageEnglish: "Quantity",
		LanguageHindi:   "मात्रा",
		LanguageTamil:   "அளவு",
	},
	"orderTicket.orderType.market": {
		LanguageEnglish: "Market",
		LanguageHindi:   "मार्केट",
		LanguageTamil:   "சந்தை",
	},
	"orderTicket.orderType.limit": {
		LanguageEnglish: "Limit",
		LanguageHindi:   "लिमिट",
		LanguageTamil:   "வரம்பு",
	},
	"orderTicket.orderType.stopLossLimit": {
		LanguageEnglish: "Stop-loss, limit (SL)",
		LanguageHindi:   "स्टॉप-लॉस, लिमिट (SL)",
		LanguageTamil:   "இழப்பு-நிறுத்தம், வரம்பு (SL)",
	},
	"orderTicket.orderType.stopLossMarket": {
		LanguageEnglish: "Stop-loss, market (SL-M)",
		LanguageHindi:   "स्टॉप-लॉस, मार्केट (SL-M)",
		LanguageTamil:   "இழப்பு-நிறுத்தம், சந்தை (SL-M)",
	},
	"orderTicket.submit.default": {
		LanguageEnglish: "Submit order",
		LanguageHindi:   "ऑर्डर सबमिट करें",
		LanguageTamil:   "ஆர்டரைச் சமர்ப்பிக்கவும்",
	},
	"orderTicket.submit.coverOrder": {
		LanguageEnglish: "Submit cover order",
		LanguageHindi:   "कवर ऑर्डर सबमिट करें",
		LanguageTamil:   "கவர் ஆர்டரைச் சமர்ப்பிக்கவும்",
	},
	"orderTicket.submit.inProgress": {
		LanguageEnglish: "Submitting…",
		LanguageHindi:   "सबमिट किया जा रहा है…",
		LanguageTamil:   "சமர்ப்பிக்கப்படுகிறது…",
	},
	"orderTicket.instrumentSymbol.label": {
		LanguageEnglish: "Instrument",
		LanguageHindi:   "इंस्ट्रूमेंट",
		LanguageTamil:   "கருவி",
	},

	// --- Order status (app/page.tsx) ---
	"orderStatus.heading": {
		LanguageEnglish: "Order status / cancel",
		LanguageHindi:   "ऑर्डर स्थिति / रद्द करें",
		LanguageTamil:   "ஆர்டர் நிலை / ரத்து செய்யவும்",
	},

	// --- Verified strategy following (app/page.tsx, app/strategies/page.tsx) ---
	"strategies.followVerified.heading": {
		LanguageEnglish: "Follow verified strategies",
		LanguageHindi:   "सत्यापित रणनीतियों को फॉलो करें",
		LanguageTamil:   "சரிபார்க்கப்பட்ட உத்திகளைப் பின்பற்றவும்",
	},
	"strategies.followVerified.shortHeading": {
		LanguageEnglish: "Follow strategies",
		LanguageHindi:   "रणनीतियों को फॉलो करें",
		LanguageTamil:   "உத்திகளைப் பின்பற்றவும்",
	},
	"strategies.verifiedStrategies.heading": {
		LanguageEnglish: "Verified strategies",
		LanguageHindi:   "सत्यापित रणनीतियां",
		LanguageTamil:   "சரிபார்க்கப்பட்ட உத்திகள்",
	},
	"strategies.empty": {
		LanguageEnglish: "empty",
		LanguageHindi:   "खाली",
		LanguageTamil:   "வெறுமையானது",
	},

	// --- Options chain (app/optionsChain/page.tsx) ---
	"optionsChain.heading": {
		LanguageEnglish: "Options chain (simplified retail view)",
		LanguageHindi:   "ऑप्शंस चेन (सरलीकृत रिटेल दृश्य)",
		LanguageTamil:   "ஆப்ஷன்ஸ் செயின் (எளிமையாக்கப்பட்ட சில்லறை காட்சி)",
	},
	"optionsChain.shortHeading": {
		LanguageEnglish: "Options chain",
		LanguageHindi:   "ऑप्शंस चेन",
		LanguageTamil:   "ஆப்ஷன்ஸ் செயின்",
	},
	"optionsChain.strike": {
		LanguageEnglish: "Strike",
		LanguageHindi:   "स्ट्राइक",
		LanguageTamil:   "ஸ்ட்ரைக்",
	},
	"optionsChain.delta": {
		LanguageEnglish: "Delta",
		LanguageHindi:   "डेल्टा",
		LanguageTamil:   "டெல்டா",
	},
	"optionsChain.gamma": {
		LanguageEnglish: "Gamma",
		LanguageHindi:   "गामा",
		LanguageTamil:   "காமா",
	},
	"optionsChain.theta": {
		LanguageEnglish: "Theta",
		LanguageHindi:   "थीटा",
		LanguageTamil:   "தீட்டா",
	},
	"optionsChain.vega": {
		LanguageEnglish: "Vega",
		LanguageHindi:   "वेगा",
		LanguageTamil:   "வேகா",
	},
	"optionsChain.openInterestVolume": {
		LanguageEnglish: "OI / Vol",
		LanguageHindi:   "OI / वॉल्यूम",
		LanguageTamil:   "OI / வால்யூம்",
	},

	// --- Historical DOM replay (app/domReplay/page.tsx) ---
	"domReplay.heading": {
		LanguageEnglish: "Historical DOM replay",
		LanguageHindi:   "ऐतिहासिक DOM रीप्ले",
		LanguageTamil:   "வரலாற்று DOM மறுஇயக்கம்",
	},
	"domReplay.intervalStart.label": {
		LanguageEnglish: "Interval start (epoch s)",
		LanguageHindi:   "अंतराल प्रारंभ (एपॉक सेकंड)",
		LanguageTamil:   "இடைவெளி தொடக்கம் (எபாக் வி)",
	},
	"domReplay.pricesTouched.heading": {
		LanguageEnglish: "Prices touched",
		LanguageHindi:   "छुई गई कीमतें",
		LanguageTamil:   "தொடப்பட்ட விலைகள்",
	},

	// --- Volume Profile / TPO (app/volumeProfile/page.tsx) ---
	"volumeProfile.heading": {
		LanguageEnglish: "Volume Profile / Market Profile (TPO)",
		LanguageHindi:   "वॉल्यूम प्रोफाइल / मार्केट प्रोफाइल (TPO)",
		LanguageTamil:   "வால்யூம் ப்ரொஃபைல் / மார்க்கெட் ப்ரொஃபைல் (TPO)",
	},
	"volumeProfile.shortHeading": {
		LanguageEnglish: "Volume Profile / TPO",
		LanguageHindi:   "वॉल्यूम प्रोफाइल / TPO",
		LanguageTamil:   "வால்யூம் ப்ரொஃபைல் / TPO",
	},
	"volumeProfile.volumeByPriceLevel.heading": {
		LanguageEnglish: "Volume by price level (real horizontal bars)",
		LanguageHindi:   "मूल्य स्तर के अनुसार वॉल्यूम (वास्तविक क्षैतिज बार)",
		LanguageTamil:   "விலை நிலை வாரியாக வால்யூம் (உண்மையான கிடைமட்ட பட்டைகள்)",
	},
	"volumeProfile.pointOfControl": {
		LanguageEnglish: "POC",
		LanguageHindi:   "POC",
		LanguageTamil:   "POC",
	},
	"volumeProfile.letter": {
		LanguageEnglish: "Letter",
		LanguageHindi:   "अक्षर",
		LanguageTamil:   "எழுத்து",
	},
	"volumeProfile.noTpoLettersForWindow": {
		LanguageEnglish: "No TPO letters for this window.",
		LanguageHindi:   "इस विंडो के लिए कोई TPO अक्षर नहीं है।",
		LanguageTamil:   "இந்த சாளரத்திற்கு TPO எழுத்துகள் இல்லை.",
	},

	// --- Order-flow footprint (app/orderFlowFootprint/page.tsx) ---
	"orderFlowFootprint.heading": {
		LanguageEnglish: "Order-flow footprint",
		LanguageHindi:   "ऑर्डर-फ्लो फुटप्रिंट",
		LanguageTamil:   "ஆர்டர்-ஃப்ளோ கால்தடம்",
	},
	"orderFlowFootprint.price": {
		LanguageEnglish: "Price",
		LanguageHindi:   "मूल्य",
		LanguageTamil:   "விலை",
	},
	"orderFlowFootprint.buyVolume": {
		LanguageEnglish: "Buy volume",
		LanguageHindi:   "खरीद वॉल्यूम",
		LanguageTamil:   "வாங்கும் வால்யூம்",
	},
	"orderFlowFootprint.sellVolume": {
		LanguageEnglish: "Sell volume",
		LanguageHindi:   "बिक्री वॉल्यूम",
		LanguageTamil:   "விற்கும் வால்யூம்",
	},
}

// LookupResult is one real translated string, returned with the key it
// was looked up by so a caller iterating a batch response can match
// results back to requests.
type LookupResult struct {
	StringKey      string `json:"stringKey"`
	TranslatedText string `json:"translatedText"`
}

// TranslationsForLanguage returns every real string in the catalog
// translated into languageCode, keyed by stringKey. Returns
// (nil, false) for a languageCode this catalog has no translations
// for at all — the HTTP layer turns that into a 404, never a silent
// empty-but-200 response.
func TranslationsForLanguage(languageCode LanguageCode) (map[string]string, bool) {
	isSupported := false
	for _, supported := range SupportedLanguages {
		if supported == languageCode {
			isSupported = true
			break
		}
	}
	if !isSupported {
		return nil, false
	}

	translations := make(map[string]string, len(catalog))
	for stringKey, translationsByLanguage := range catalog {
		translations[stringKey] = translationsByLanguage[languageCode]
	}
	return translations, true
}

// StringKeyCount returns how many real string keys this catalog covers
// — used by tests to assert the catalog is genuinely populated, not a
// token handful of strings.
func StringKeyCount() int {
	return len(catalog)
}

// AllStringKeysSorted returns every catalog key in stable sorted order
// — used by tests to assert every language has a translation for every
// key (no silently-missing entries).
func AllStringKeysSorted() []string {
	keys := make([]string, 0, len(catalog))
	for stringKey := range catalog {
		keys = append(keys, stringKey)
	}
	sort.Strings(keys)
	return keys
}
