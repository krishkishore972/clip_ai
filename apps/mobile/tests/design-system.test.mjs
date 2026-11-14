import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import vm from "node:vm";
import {
  nativeColors,
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
    textVariants.hero,
    "font-display-bold text-[42px] font-normal leading-[46px]",
  );
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

test("native props resolve to semantic tokens, never literal hex values", () => {
  const rgbToHex = (rgb) =>
    `#${rgb
      .split(" ")
      .map((channel) => Number(channel).toString(16).padStart(2, "0"))
      .join("")
      .toUpperCase()}`;
  assert.equal(nativeColors.background, rgbToHex(palettes.dark.background));
  assert.equal(nativeColors.card, rgbToHex(palettes.dark.card));
  assert.equal(nativeColors.secondary, rgbToHex(palettes.dark.secondary));
  assert.equal(nativeColors.border, rgbToHex(palettes.dark.border));
  assert.equal(nativeColors.primary, rgbToHex(palettes.dark.primary));
  assert.equal(nativeColors.foreground, rgbToHex(palettes.dark.foreground));
  assert.equal(
    nativeColors.mutedForeground,
    rgbToHex(palettes.dark["muted-foreground"]),
  );

  for (const file of [
    "../src/app/_layout.tsx",
    "../src/components/auth-gate.tsx",
    "../src/components/floating-tab-bar.tsx",
  ]) {
    const source = readFileSync(new URL(file, import.meta.url), "utf8");
    assert.ok(
      !/#[0-9a-f]{6}\b/i.test(source),
      `${file} must use nativeColors instead of literal colors`,
    );
  }
});

test("product UI uses semantic color tokens instead of raw palette classes", () => {
  const files = [
    "../src/app/_layout.tsx",
    "../src/app/index.tsx",
    "../src/app/account.tsx",
    "../src/app/upload.tsx",
    "../src/components/auth-gate.tsx",
    "../src/components/upload-form.tsx",
    "../src/components/floating-tab-bar.tsx",
    "../src/design-system/index.tsx",
  ];
  const rawPalette =
    /\b(?:bg|text|border|ring|from|to|via|fill|stroke)-(?:black|white|gray|grey|neutral|zinc|slate|red|green|blue|yellow|orange|purple|pink|teal)\b/;
  for (const file of files) {
    const source = readFileSync(new URL(file, import.meta.url), "utf8");
    assert.ok(
      !rawPalette.test(source),
      `${file} must use a semantic token, not a raw palette class`,
    );
  }
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

test("brand lockup uses the generated logo asset on both auth surfaces", () => {
  const png = readFileSync(
    new URL("../assets/images/chorus-logo.png", import.meta.url),
  );
  assert.equal(png.subarray(1, 4).toString(), "PNG");
  assert.equal(png.readUInt32BE(16), 1024);
  assert.equal(png.readUInt32BE(20), 1024);

  const svg = readFileSync(
    new URL("../assets/brand/chorus-logo.svg", import.meta.url),
    "utf8",
  );
  assert.ok(
    svg.includes('fill="#F0F424"'),
    "editable source keeps brand yellow",
  );

  const gate = readFileSync(
    new URL("../src/components/auth-gate.tsx", import.meta.url),
    "utf8",
  );
  assert.equal((gate.match(/chorus-logo\.png/g) ?? []).length, 1);
  assert.ok(gate.includes("CUT TO THE MOMENT."));

  const account = readFileSync(
    new URL("../src/app/account.tsx", import.meta.url),
    "utf8",
  );
  assert.equal((account.match(/chorus-logo\.png/g) ?? []).length, 1);
});

test("authenticated shell groups Home, Upload and Account in a floating pill", () => {
  const layout = readFileSync(
    new URL("../src/app/_layout.tsx", import.meta.url),
    "utf8",
  );
  assert.ok(layout.includes('from "expo-router/js-tabs"'));
  assert.ok(!layout.includes("native-tabs"));
  assert.ok(
    layout.includes("tabBar={(props) => <FloatingTabBar {...props} />}"),
  );

  // Upload is the middle tab and Account is the right-most one.
  const upload = layout.indexOf('<Tabs.Screen name="upload"');
  const account = layout.indexOf('<Tabs.Screen name="account"');
  const homeTab = layout.indexOf('<Tabs.Screen name="index"');
  assert.ok(homeTab < upload, "Home must be the first tab");
  assert.ok(upload < account, "Account must come after Upload");

  assert.ok(!layout.includes("BottomBar"));
  assert.equal(
    textVariants.screenTitle,
    "font-display-bold text-[34px] font-normal leading-[40px]",
  );

  const bar = readFileSync(
    new URL("../src/components/floating-tab-bar.tsx", import.meta.url),
    "utf8",
  );
  assert.ok(bar.includes('accessibilityRole="tab"'));
  assert.ok(bar.includes("bg-secondary"));
  assert.ok(bar.includes("rounded-full"));
  assert.ok(bar.includes("bg-primary"));

  // The bar stays fixed at the bottom: it must never be animated away, and its
  // opaque band must hide the content that scrolls underneath it.
  assert.ok(bar.includes("bottom-0"));
  assert.ok(bar.includes("bg-background"));
  assert.ok(!bar.includes("Animated"));
  assert.ok(!bar.includes("translateY"));
  const order = bar.indexOf("index:");
  const uploadEntry = bar.indexOf("upload:");
  const accountEntry = bar.indexOf("account:");
  assert.ok(order < uploadEntry && uploadEntry < accountEntry);

  for (const file of ["index", "account", "upload"]) {
    assert.ok(
      !readFileSync(
        new URL(`../src/app/${file}.tsx`, import.meta.url),
        "utf8",
      ).includes("bottom-bar"),
      `${file} must not import the removed custom bar`,
    );
  }

  const home = readFileSync(
    new URL("../src/app/index.tsx", import.meta.url),
    "utf8",
  );
  assert.ok(home.includes("No projects yet"));
  assert.ok(home.includes("projects-empty.png"));
  assert.ok(home.includes('router.navigate("/upload")'));
  assert.match(home, /label="Upload a video"\s+variant="secondary"/);
  assert.ok(home.includes('router.navigate("/account")'));

  const art = readFileSync(
    new URL("../assets/images/projects-empty.png", import.meta.url),
  );
  assert.equal(art.subarray(1, 4).toString(), "PNG");
});
