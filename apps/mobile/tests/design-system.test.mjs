import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import vm from "node:vm";
import {
  progressPercent,
  textTones,
  textVariants,
} from "../src/design-system/tokens.ts";

const css = readFileSync(new URL("../src/global.css", import.meta.url), "utf8");
const roots = [...css.matchAll(/:root\s*\{([^}]+)\}/g)];
assert.equal(roots.length, 2, "Expected light and dark root palettes");
const palettes = Object.fromEntries(
  roots.map((root, i) => [
    i === 0 ? "light" : "dark",
    Object.fromEntries(
      [...root[1].matchAll(/--([\w-]+):\s*(\d+ \d+ \d+);/g)].map((match) => [
        match[1],
        match[2],
      ]),
    ),
  ]),
);

function luminance(rgb) {
  assert.ok(rgb, "Missing CSS color token");
  const channels = rgb.split(" ").map((value) => {
    const channel = Number(value) / 255;
    return channel <= 0.04045
      ? channel / 12.92
      : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
}

function contrast(a, b) {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (light + 0.05) / (dark + 0.05);
}

for (const [mode, palette] of Object.entries(palettes)) {
  test(`${mode}: semantic text and action colors meet normal-text contrast`, () => {
    for (const foreground of [
      "foreground",
      "muted-foreground",
      "primary-ink",
      "destructive",
      "success",
    ]) {
      for (const background of ["background", "card", "secondary"]) {
        assert.ok(
          contrast(palette[foreground], palette[background]) >= 4.5,
          `${foreground}/${background}`,
        );
      }
    }
    assert.ok(contrast(palette["primary-foreground"], palette.primary) >= 4.5);
    // Yellow is a fill, never body text on white or a white-on-yellow label.
    assert.ok(contrast(palette.primary, "0 0 0") >= 3);
    assert.ok(contrast(palette["accent-foreground"], palette.accent) >= 4.5);
    assert.ok(
      contrast(palette["highlight-foreground"], palette.highlight) >= 4.5,
    );
  });
}

test("progress is an integer percentage, safely bounded at the presentation boundary", () => {
  for (const [value, expected] of [
    [-10, 0],
    [0, 0],
    [42.9, 42],
    [100, 100],
    [101, 100],
    [NaN, 0],
    [Infinity, 0],
    [-Infinity, 0],
  ]) {
    assert.equal(progressPercent(value), expected);
  }
});

test("every text tone maps to a CSS semantic color utility", () => {
  for (const className of Object.values(textTones)) {
    assert.ok(css.includes(`--color-${className.slice(5)}:`), className);
  }
  assert.equal(
    textVariants.caption,
    "font-display text-sm font-normal leading-5",
  );
  assert.equal(
    textVariants.label,
    "font-display-medium text-sm font-normal leading-5",
  );
});

test("user-selected brand colors are exact in both palettes", () => {
  for (const palette of Object.values(palettes)) {
    assert.equal(palette.primary, "240 244 36");
    assert.equal(palette["primary-foreground"], "0 0 0");
    assert.equal(palette.accent, "197 224 255");
    assert.equal(palette.highlight, "217 75 245");
  }
  assert.equal(palettes.dark.background, "0 0 0");
  assert.equal(palettes.dark.foreground, "255 255 255");
});

test("SF Pro faces resolve to supplied upright assets and matching CSS aliases", () => {
  const fontMap = readFileSync(
    new URL("../src/design-system/fonts.ts", import.meta.url),
    "utf8",
  );
  const faces = [
    ...fontMap.matchAll(
      /"(SFProDisplay-[^"]+)":\s*require\(\s*"([^"]+)"\s*\)/g,
    ),
  ];
  assert.equal(faces.length, 3);
  for (const [, alias, path] of faces) {
    assert.ok(css.includes(`: ${alias};`));
    assert.ok(!path.includes("ITALIC"));
    const bytes = readFileSync(
      new URL(path, new URL("../src/design-system/fonts.ts", import.meta.url)),
    );
    assert.equal(bytes.subarray(0, 4).toString(), "OTTO");
  }
});

test("native asset resolver covers NativeWind redirects but never recurses or changes web", () => {
  const require = createRequire(import.meta.url);
  const { withAssetRegistry } = require("../scripts/metro-asset-registry.cjs");
  const compat = "/app/nativewind-components.cjs";
  const normal = { type: "sourceFile", filePath: "/normal" };
  const context = {
    originModulePath: "/app/consumer.js",
    resolveRequest: () => normal,
  };
  const resolve = withAssetRegistry(compat);
  assert.deepEqual(resolve(context, "react-native-css/components", "android"), {
    type: "sourceFile",
    filePath: compat,
  });
  assert.deepEqual(resolve(context, "react-native-css/components", "ios"), {
    type: "sourceFile",
    filePath: compat,
  });
  assert.equal(resolve(context, "react-native-css/components", "web"), normal);
  assert.equal(
    resolve(
      { ...context, originModulePath: compat },
      "react-native-css/components",
      "android",
    ),
    normal,
  );
  assert.equal(resolve(context, "react-native/index.js", "android"), normal);
  const parentResult = { type: "sourceFile", filePath: "/parent" };
  assert.equal(
    withAssetRegistry(compat, () => parentResult)(context, "other", "android"),
    parentResult,
  );
});

test("compatibility entry preserves lazy UI exports and forwards the real registry", () => {
  const components = {};
  let readCount = 0;
  Object.defineProperty(components, "View", {
    enumerable: true,
    get: () => {
      readCount++;
      return "NativeWindView";
    },
  });
  const registry = { getAssetByID: (id) => ({ id }) };
  const context = {
    module: { exports: {} },
    require: (path) => {
      if (path === "react-native-css/components") return components;
      if (path === "react-native/index.js") return { AssetRegistry: registry };
      throw new Error(`Unexpected dependency: ${path}`);
    },
  };
  vm.runInNewContext(
    readFileSync(
      new URL("../src/lib/nativewind-components.cjs", import.meta.url),
      "utf8",
    ),
    context,
  );
  assert.equal(readCount, 0);
  assert.equal(context.module.exports.View, "NativeWindView");
  assert.equal(context.module.exports.AssetRegistry, registry);
  assert.equal(Object.hasOwn(components, "AssetRegistry"), false);
});
