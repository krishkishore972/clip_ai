const { getDefaultConfig } = require("expo/metro-config");
const { withNativewind } = require("nativewind/metro");
const { withAssetRegistry } = require("./scripts/metro-asset-registry.cjs");

const config = getDefaultConfig(__dirname);
config.resolver.resolveRequest = withAssetRegistry(
  require.resolve("./src/lib/nativewind-components.cjs"),
  config.resolver.resolveRequest,
);

module.exports = withNativewind(config, { inlineRem: 16 });
