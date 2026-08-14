// Mercurius / terminal — command bar UI. FEATURES.md §10 "[P2] Command
// bar / hotkey system (`AAPL DES <GO>` style)".
//
// Wraps `commandBarParser.ts`'s pure parser with a real input box +
// keyboard handling + a dispatch callback the workspace shell uses to open
// the right widget/tile for the parsed command. A global hotkey
// (backtick, matching Bloomberg's own <GO>-adjacent muscle memory of "a
// dedicated always-available key focuses the command line") focuses this
// input from anywhere in the app.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useRef, useState } from "react";
import { parseCommandBarInput, type ParsedCommandBarCommand } from "./commandBarParser";

const COMMAND_BAR_FOCUS_HOTKEY = "`";

export function CommandBar(props: { onCommandDispatched: (command: ParsedCommandBarCommand) => void }) {
  const [inputValue, setInputValue] = useState("");
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    function handleGlobalKeyDown(keyboardEvent: KeyboardEvent) {
      const activeElement = document.activeElement;
      const isAlreadyTypingSomewhereElse =
        activeElement instanceof HTMLInputElement || activeElement instanceof HTMLTextAreaElement;
      if (keyboardEvent.key === COMMAND_BAR_FOCUS_HOTKEY && !isAlreadyTypingSomewhereElse) {
        keyboardEvent.preventDefault();
        inputRef.current?.focus();
      }
    }
    window.addEventListener("keydown", handleGlobalKeyDown);
    return () => window.removeEventListener("keydown", handleGlobalKeyDown);
  }, []);

  function handleSubmit(formSubmitEvent: React.FormEvent<HTMLFormElement>) {
    formSubmitEvent.preventDefault();
    // A bare Enter press implicitly means <GO> even if the user didn't
    // type it — matching how a real Bloomberg terminal's Enter key works.
    const effectiveInput = /\bgo\b|<go>/i.test(inputValue) ? inputValue : `${inputValue} GO`;
    const result = parseCommandBarInput(effectiveInput);
    if (result.wasParseSuccessful) {
      setErrorMessage(null);
      props.onCommandDispatched(result.command);
      setInputValue("");
    } else {
      setErrorMessage(result.errorMessage);
    }
  }

  return (
    <form className="commandBar" onSubmit={handleSubmit}>
      <span className="commandBar__prompt">›</span>
      <input
        ref={inputRef}
        className="commandBar__input"
        type="text"
        placeholder="AAPL DES <GO>  (press ` to focus)"
        value={inputValue}
        onChange={(changeEvent) => setInputValue(changeEvent.target.value)}
        spellCheck={false}
        autoComplete="off"
      />
      {errorMessage && <span className="commandBar__error">{errorMessage}</span>}
    </form>
  );
}
