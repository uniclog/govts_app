import {useState} from "react";
import {
    builtinThemes,
    fontFamilies,
    fontSizes,
    fontStack,
    stageBackdrops,
    themePalettes,
    type Appearance,
    type AppearanceOptions,
    type ThemePalette,
} from "../../appearance";
import {errorCode, errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {StageScene} from "./StageScene";

export function InterfaceSettings({appearance, onTheme, onChange}: {
    appearance: Appearance;
    onTheme: (theme: string) => Promise<void>;
    onChange: (patch: AppearanceOptions) => void;
}) {
    const {m} = useLocale();
    const [notice, setNotice] = useState("");

    const commit = (patch: AppearanceOptions) => {
        try {
            onChange(patch);
            setNotice("");
        } catch (error) {
            setNotice(error instanceof Error ? error.message : errorCode("theme-save-failed"));
        }
    };

    return <>
        <div className="choice-heading">{m.theme.heading}</div>
        <div className="theme-gallery" role="group" aria-label={m.theme.gallery}>
            {builtinThemes.map((theme) => <ThemeSwatch key={theme} label={m.theme.names[theme]} palette={themePalettes[theme]}
                                                        pressed={appearance.theme === theme}
                                                        onClick={() => void onTheme(theme)}/>)}
        </div>
        <div className="appearance-options">
            <div>
                <div className="choice-heading" id="font-family-label">{m.theme.font}</div>
                <div className="font-grid" role="group" aria-labelledby="font-family-label">
                    {fontFamilies.map((font) => <button type="button" className="font-option" key={font}
                                                         aria-pressed={appearance.fontFamily === font}
                                                         style={{fontFamily: fontStack(font)}}
                                                         onClick={() => commit({fontFamily: font})}>
                        <strong>{font === "default" ? m.theme.fontDefault : font}</strong>
                        <span>{m.theme.sample}</span>
                    </button>)}
                </div>
            </div>
            <div>
                <div className="choice-heading" id="font-size-label">{m.theme.fontSize}</div>
                <div className="size-row" role="group" aria-labelledby="font-size-label">
                    {fontSizes.map((size) => <button type="button" className="size-chip" key={size} aria-pressed={appearance.fontSize === size}
                                                      style={{fontSize: size}}
                                                      onClick={() => commit({fontSize: size})}>{size}</button>)}
                </div>
                <p className="theme-note">{m.theme.sizeNote}</p>
            </div>
            <div>
                <div className="choice-heading" id="backdrop-label">{m.theme.backdrop}</div>
                <div className="backdrop-grid" role="group" aria-labelledby="backdrop-label">
                    {stageBackdrops.map((backdrop) => <button type="button" className="backdrop-option" key={backdrop}
                                                               aria-pressed={appearance.stageBackdrop === backdrop}
                                                               onClick={() => commit({stageBackdrop: backdrop})}>
                        <span className="backdrop-preview" data-backdrop={backdrop}>
                            <StageScene backdrop={backdrop}/>
                            <i className="backdrop-avatar"/>
                        </span>
                        <span>{m.theme.backdrops[backdrop]}</span>
                    </button>)}
                </div>
            </div>
            {notice && <div className="form-error" role="alert">{errorText(notice, m)}</div>}
        </div>
    </>;
}

function ThemeSwatch({label, palette, pressed, onClick}: {
    label: string;
    palette: ThemePalette;
    pressed: boolean;
    onClick: () => void;
}) {
    const {m} = useLocale();
    return <div className="theme-swatch">
        <button type="button" className="theme-swatch-open" aria-pressed={pressed} onClick={onClick}>
            <span className="theme-swatch-canvas" style={{background: palette.window}}>
                <span className="theme-swatch-card" style={{background: palette.card, borderColor: palette.line, color: palette.text}}>
                    <i style={{background: palette.accent}}/>
                    <b>{m.theme.swatch}</b>
                </span>
            </span>
            <span className="theme-swatch-label">{label}</span>
        </button>
    </div>;
}
