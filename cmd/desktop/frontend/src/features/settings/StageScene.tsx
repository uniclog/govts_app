import {memo, type CSSProperties} from "react";
import type {StageBackdrop} from "../../appearance";

function mulberry32(seed: number) {
    return () => {
        seed |= 0;
        seed = (seed + 0x6D2B79F5) | 0;
        let value = Math.imul(seed ^ (seed >>> 15), 1 | seed);
        value = (value + Math.imul(value ^ (value >>> 7), 61 | value)) ^ value;
        return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
    };
}

const pines: {x: number; h: number; w: number; far: boolean}[] = [];
{
    const random = mulberry32(0x51a7c3);
    let x = -16;
    while (x < 1700) {
        const h = 18 + random() * 64;
        const w = 6 + random() * 28;
        pines.push({x, h: Math.round(h), w: Math.round(w), far: random() > 0.58});
        const clearing = random() < 0.14;
        x += (clearing ? 22 + random() * 36 : 2 + random() * 14);
    }
}

// Качание: фаза зависит от положения дерева, поэтому по лесу проходит волна ветра.
// Свой генератор, чтобы не сдвинуть раскладку деревьев.
const pineSway = (() => {
    const random = mulberry32(0x7e3a51);
    return pines.map((pine) => ({
        animationDuration: `${(4.2 + random() * 1.6).toFixed(2)}s`,
        animationDelay: `${(-(pine.x * 0.035) - random() * 0.7).toFixed(2)}s`,
    }));
})();

const buildings: {x: number; h: number; w: number; far: boolean}[] = [];
{
    const random = mulberry32(0xc17e11);
    let x = -6;
    while (x < 1700) {
        const h = 14 + Math.pow(random(), 1.4) * 78;
        const w = 7 + random() * 20;
        buildings.push({x, h: Math.round(h), w: Math.round(w), far: random() > 0.5});
        x += w * 0.2 + (random() < 0.18 ? 8 + random() * 16 : random() * 3);
    }
}

// Окна зажигаются и гаснут восемью группами: анимируются восемь слоёв, а не сотни элементов.
const cityLightGroups = 8;
const cityLights: {x: number; y: number}[][] = Array.from({length: cityLightGroups}, () => []);
{
    const random = mulberry32(0x2f6b19);
    for (const building of buildings) {
        if (building.far) continue;
        for (let y = 5; y < building.h - 4; y += 7) {
            if (random() < 0.55) cityLights[Math.floor(random() * cityLightGroups)].push({x: building.x + 2 + random() * (building.w - 5), y});
        }
    }
}

// Звёзды — точки в 1–2 px. Группа 0 светит ровно, остальные мерцают каждая в своём ритме.
const starGroups: {x: number; y: number; size: number; opacity: number; bright: boolean}[][] = [[], [], [], []];
{
    const random = mulberry32(0x5ea7f1);
    for (let index = 0; index < 110; index++) {
        const roll = random();
        const bright = roll > 0.95;
        starGroups[Math.floor(random() * starGroups.length)].push({
            x: random() * 100,
            y: random() * 100,
            size: bright ? 2 : roll > 0.75 ? 1.5 : 1,
            opacity: bright ? 1 : 0.3 + random() * 0.55,
            bright,
        });
    }
}

// Метеоры в разных частях неба. Периоды взаимно простые, поэтому порядок появления не повторяется.
const meteors: {x: number; y: number; angle: number; period: number; delay: number}[] = [
    {x: 8, y: 8, angle: 24, period: 18, delay: -1.4},
    {x: 62, y: 4, angle: 156, period: 23, delay: -8},
    {x: 30, y: 34, angle: 18, period: 25, delay: -16},
    {x: 84, y: 26, angle: 148, period: 31, delay: -4},
    {x: 46, y: 12, angle: 34, period: 37, delay: -27},
    {x: 14, y: 52, angle: 12, period: 41, delay: -12},
    {x: 72, y: 46, angle: 162, period: 49, delay: -36},
];

function PineLayer({far}: {far: boolean}) {
    return <div className={far ? "stage-pines stage-pines-far" : "stage-pines"}>
        {pines.map((pine, index) => pine.far === far && <i key={index} className={far ? "stage-pine stage-pine-far" : "stage-pine"} style={{left: `${pine.x}cqh`, height: `${pine.h}cqh`, width: `${pine.w}cqh`, ...pineSway[index]}}/>)}
    </div>;
}

// memo: сцена не должна пересобираться на каждое событие речи участников.
// Туман, огни, метеоры и блик видны только в движении (opacity: 0 в CSS).
export const StageScene = memo(function StageScene({backdrop}: {backdrop: StageBackdrop}) {
    if (backdrop === "rings") return <div className="workspace-stage-art" aria-hidden="true"><i/><i/><i/><i/></div>;
    return <div className={`stage-scene stage-scene-${backdrop}`} aria-hidden="true">
        {backdrop === "mountains" && <><div className="stage-ridge"/><i className="stage-mist stage-mist-a"/><i className="stage-mist stage-mist-b"/></>}
        {backdrop === "pines" && <><PineLayer far/><PineLayer far={false}/></>}
        {backdrop === "clouds" && <><i className="stage-cloud stage-cloud-a"/><i className="stage-cloud stage-cloud-b"/><i className="stage-cloud stage-cloud-c"/></>}
        {backdrop === "horizon" && <><i className="stage-horizon-glow"/><div className="stage-sun"/></>}
        {backdrop === "city" && <>
            <div className="stage-city">{buildings.map((building, index) => <i key={index} className={building.far ? "stage-building stage-building-far" : "stage-building"} style={{left: `${building.x}cqh`, height: `${building.h}cqh`, width: `${building.w}cqh`}}/>)}</div>
            {cityLights.map((group, index) => <div key={index} className="stage-city-lights" style={{animationDelay: `${index * -5}s`}}>
                {group.map((light, key) => <i key={key} style={{left: `${light.x}cqh`, bottom: `${light.y}cqh`}}/>)}
            </div>)}
        </>}
        {backdrop === "stars" && <>
            {starGroups.map((group, index) => <div key={index} className="stage-stars" style={{animationDuration: `${4.2 + index * 2.3}s`, animationDelay: `${index * -1.75}s`}}>
                {group.map((star, key) => <i key={key} className={star.bright ? "stage-star-bright" : undefined} style={{left: `${star.x}%`, top: `${star.y}%`, width: `${star.size}px`, height: `${star.size}px`, opacity: star.opacity}}/>)}
            </div>)}
            {meteors.map((meteor, index) => <i key={index} className="stage-meteor" style={{left: `${meteor.x}%`, top: `${meteor.y}%`, "--meteor-angle": `${meteor.angle}deg`, animationDuration: `${meteor.period}s`, animationDelay: `${meteor.delay}s`} as CSSProperties}/>)}
        </>}
        {backdrop === "plain" && <i className="stage-sheen"/>}
    </div>;
});
