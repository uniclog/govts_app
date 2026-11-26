import {createContext, useContext, useEffect, useRef, useState, type ReactNode} from "react";
import {desktopAPI} from "../api";
import {fallbackLocaleId, localeById, locales, type Messages} from "./index";

type LocaleContextValue = {
    locale: string;
    htmlLang: string;
    setLocale: (id: string) => void;
    m: Messages;
};

const fallback = localeById(fallbackLocaleId);
const LocaleContext = createContext<LocaleContextValue>({
    locale: fallback.id,
    htmlLang: fallback.htmlLang,
    setLocale: () => undefined,
    m: fallback.messages,
});

export function LocaleProvider({children}: {children: ReactNode}) {
    const [locale, setLocaleState] = useState(fallbackLocaleId);
    const touched = useRef(false);
    const current = localeById(locale);

    useEffect(() => {
        let active = true;
        void desktopAPI.locale()
            .then((value) => {
                if (active && !touched.current && locales.some((item) => item.id === value)) setLocaleState(value);
            })
            .catch(() => undefined);
        return () => { active = false; };
    }, []);

    useEffect(() => {
        document.documentElement.lang = current.htmlLang;
    }, [current.htmlLang]);

    // Hotkey notifications appear while the window is hidden, so the desktop
    // shell keeps the current language's titles instead of asking for them.
    useEffect(() => {
        void desktopAPI.setHotkeyNotices(current.messages.hotkeyNotices).catch(() => undefined);
    }, [current.messages]);

    const setLocale = (id: string) => {
        if (!locales.some((item) => item.id === id) || id === locale) return;
        touched.current = true;
        const previous = locale;
        setLocaleState(id);
        void desktopAPI.setLocale(id).catch((error: unknown) => {
            // The browser preview rejects binding calls with an empty Error.
            // A desktop save failure carries a message, and that choice rolls back.
            const message = error instanceof Error ? error.message : String(error ?? "");
            if (error instanceof ReferenceError || message === "") return;
            setLocaleState(previous);
        });
    };

    return <LocaleContext.Provider value={{locale: current.id, htmlLang: current.htmlLang, setLocale, m: current.messages}}>
        {children}
    </LocaleContext.Provider>;
}

export function useLocale(): LocaleContextValue {
    return useContext(LocaleContext);
}

function LanguageMark() {
    return <svg className="locale-header-mark" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="12" cy="12" r="9"/>
        <path d="M3 12h18"/>
        <path d="M12 3c2.6 2.7 3.9 5.7 3.9 9s-1.3 6.3-3.9 9c-2.6-2.7-3.9-5.7-3.9-9s1.3-6.3 3.9-9Z"/>
    </svg>;
}

export function LocaleSwitch({labelledBy, variant = "settings"}: {labelledBy?: string; variant?: "settings" | "header"}) {
    const {locale, setLocale, m} = useLocale();
    return <div className={variant === "header" ? "locale-header" : "locale-switch"} role="group" aria-label={m.language.group} aria-labelledby={labelledBy}>
        {variant === "header" && <LanguageMark/>}
        {locales.map((item) => <button type="button" key={item.id} aria-pressed={locale === item.id} aria-label={item.nativeName} title={item.nativeName}
            onClick={() => setLocale(item.id)}>{variant === "header" ? item.shortName : item.nativeName}</button>)}
    </div>;
}
