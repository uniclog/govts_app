import {useEffect, useRef, useState} from "react";
import {desktopAPI} from "../../api";
import {Icon} from "../../components/Icon";
import {themeColor} from "../../appearance";
import {rawError} from "../../locales";
import {useLocale} from "../../locales/Locale";

export function SoundPackPreview() {
    const {m} = useLocale();
    const [playing, setPlaying] = useState(false);
    const [error, setError] = useState("");
    const generation = useRef(0);
    const play = async () => {
        if (playing) {
            generation.current += 1;
            setPlaying(false);
            await desktopAPI.stopSoundPreview().catch(() => undefined);
            return;
        }
        const token = ++generation.current;
        setPlaying(true);
        setError("");
        try {
            await desktopAPI.previewSoundPack();
        } catch (cause) {
            if (generation.current === token) setError(rawError(cause));
        } finally {
            if (generation.current === token) setPlaying(false);
        }
    };
    return <div className="monitor-setting">
        <button type="button" className={`monitor-button ${playing ? "active" : ""}`} aria-pressed={playing} onClick={() => void play()}>
            <Icon name="sound"/>{playing ? m.settings.previewPackStop : m.settings.previewPack}
        </button>
        <p className="device-hint">{m.settings.previewPackHint}</p>
        {error && <p className="device-hint" role="alert">{error}</p>}
    </div>;
}

export function SensitivitySlider({id = "rnnoise-sensitivity", max = 1, label, description, value, disabled, onChange, onCommit}: {
    id?: string;
    max?: number;
    label: string;
    description: string;
    value: number;
    disabled: boolean;
    onChange: (value: number) => void;
    onCommit: (value: number) => Promise<void>;
}) {
    const commit = (value: string) => void onCommit(Number(value));
    return <div className={`sensitivity-setting slider-setting ${disabled ? "disabled" : ""}`}>
        <div><label className="setting-label" htmlFor={id}>{label}</label>
            <output htmlFor={id}>{Math.round(value * 100)}%</output>
        </div>
        <p>{description}</p>
        <input id={id} aria-label={label} type="range" min="0" max={max} step="0.01"
               value={value} disabled={disabled}
               onChange={(event) => onChange(Number(event.target.value))}
               onPointerUp={(event) => commit(event.currentTarget.value)}
               onKeyUp={(event) => commit(event.currentTarget.value)}/>
    </div>;
}

export function AudioWaveform({sensitivity, disabled, onSensitivityChange, onSensitivityCommit}: {
    sensitivity: number;
    disabled: boolean;
    onSensitivityChange: (value: number) => void;
    onSensitivityCommit: (value: number) => Promise<void>
}) {
    const {m} = useLocale();
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const thresholdRef = useRef<HTMLDivElement>(null);
    const targetRef = useRef({input: 0, processed: 0, transmitted: 0, updatedAt: 0});
    const thresholdPosition = 1 - sensitivity;
    const thresholdLevel = (40 - 35 * sensitivity) / 60;
    const thresholdLevelRef = useRef(thresholdLevel);
    const sensitivityFromPosition = (position: number) => 1 - position;

    useEffect(() => {
        thresholdLevelRef.current = thresholdLevel;
        thresholdRef.current?.style.setProperty("--threshold", `${thresholdPosition * 100}%`);
    }, [thresholdLevel, thresholdPosition]);

    useEffect(() => desktopAPI.onAudioMeter((sample) => {
        targetRef.current = {
            input: Math.max(0, Math.min(1, sample.input)),
            processed: Math.max(0, Math.min(1, sample.processed ?? sample.input)),
            transmitted: Math.max(0, Math.min(1, sample.transmitted)),
            updatedAt: performance.now(),
        };
    }), []);

    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas) return;
        const context = canvas.getContext("2d");
        if (!context) return;
        const points = 96;
        const rejectedHistory = Array<number>(points).fill(0);
        const transmittedHistory = Array<number>(points).fill(0);
        let currentInput = 0;
        let currentProcessed = 0;
        let currentTransmitted = 0;
        let previousSample = performance.now();
        let animationFrame = 0;

        const resize = () => {
            const bounds = canvas.getBoundingClientRect();
            const scale = window.devicePixelRatio || 1;
            canvas.width = Math.max(1, Math.round(bounds.width * scale));
            canvas.height = Math.max(1, Math.round(bounds.height * scale));
            context.setTransform(scale, 0, 0, scale, 0, 0);
        };
        const observer = new ResizeObserver(resize);
        observer.observe(canvas);
        resize();

        const ribbon = (history: number[], center: number, height: number, fill: string, stroke: string) => {
            const width = canvas.clientWidth;
            const step = width / Math.max(1, history.length - 1);
            context.beginPath();
            history.forEach((value, index) => {
                const y = center - Math.max(0.6, value * height);
                if (index === 0) context.moveTo(0, y);
                else context.lineTo(index * step, y);
            });
            for (let index = history.length - 1; index >= 0; index--) {
                context.lineTo(index * step, center + Math.max(0.6, history[index] * height));
            }
            context.closePath();
            context.fillStyle = fill;
            context.fill();
            context.strokeStyle = stroke;
            context.lineWidth = 1;
            context.stroke();
        };

        const draw = (now: number) => {
            const target = targetRef.current;
            const stale = now - target.updatedAt > 120;
            const inputTarget = stale ? 0 : target.input;
            const processedTarget = stale ? 0 : target.processed;
            const transmittedTarget = stale ? 0 : target.transmitted;
            currentInput += (inputTarget - currentInput) * (inputTarget > currentInput ? 0.42 : 0.16);
            currentProcessed += (processedTarget - currentProcessed) * (processedTarget > currentProcessed ? 0.45 : 0.035);
            currentTransmitted += (transmittedTarget - currentTransmitted) * (transmittedTarget > currentTransmitted ? 0.48 : 0.2);
            if (thresholdRef.current) {
                const levelOnThresholdScale = Math.max(0, Math.min(1, (currentProcessed - 1 / 12) / (7 / 12)));
                thresholdRef.current.style.setProperty("--level", `${levelOnThresholdScale * 100}%`);
                thresholdRef.current.classList.toggle("open", processedTarget >= thresholdLevelRef.current && processedTarget > 0.003);
            }
            if (now - previousSample >= 25) {
                const rejected = Math.max(0, currentInput - currentTransmitted);
                rejectedHistory.shift();
                rejectedHistory.push(rejected);
                transmittedHistory.shift();
                transmittedHistory.push(currentTransmitted);
                previousSample = now;
            }
            const width = canvas.clientWidth;
            const height = canvas.clientHeight;
            context.clearRect(0, 0, width, height);
            const waveCenter = height * 0.5;
            context.beginPath();
            context.moveTo(0, waveCenter);
            context.lineTo(width, waveCenter);
            context.strokeStyle = themeColor("--muted", 0.1);
            context.lineWidth = 1;
            context.stroke();
            ribbon(rejectedHistory, waveCenter, height * 0.38, themeColor("--muted", 0.18), themeColor("--muted", 0.34));
            ribbon(transmittedHistory, waveCenter, height * 0.38, themeColor("--accent", 0.24), themeColor("--accent", 0.9));
            animationFrame = requestAnimationFrame(draw);
        };
        animationFrame = requestAnimationFrame(draw);
        return () => {
            cancelAnimationFrame(animationFrame);
            observer.disconnect();
        };
    }, []);

    const updateThreshold = (position: number, commit: boolean) => {
        const value = sensitivityFromPosition(position);
        onSensitivityChange(value);
        if (commit) void onSensitivityCommit(value);
    };
    return <>
        <div className={`threshold-meter ${disabled ? "disabled" : ""}`} ref={thresholdRef}><span
            className="threshold-track"><span className="threshold-level"/></span><span className="threshold-marker"
                                                                                        aria-hidden="true"/><input
            aria-label={m.settings.threshold} type="range" min="0" max="1" step="0.01" value={thresholdPosition}
            disabled={disabled} onChange={(event) => updateThreshold(Number(event.target.value), false)}
            onPointerUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}
            onKeyUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}/></div>
        <div className="audio-waveform">
            <div className="audio-waveform-heading"><span className="setting-label">{m.settings.activity}</span><span
                className="wave-legend"><i className="transmitted"/>{m.settings.transmitted}<i className="rejected"/>{m.settings.rejected}</span>
            </div>
            <canvas ref={canvasRef} role="img" aria-label={m.settings.waveform}/>
        </div>
    </>;
}
