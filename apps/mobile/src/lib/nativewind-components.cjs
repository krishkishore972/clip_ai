// react-native-css 3.1.0-rc.0's native CJS entry omits RN 0.88 AssetRegistry.
// Preserve lazy component getters without mutating the dependency's exports.
const components = require("react-native-css/components");
const descriptors = Object.getOwnPropertyDescriptors(components);
descriptors.AssetRegistry = {
  enumerable: true,
  get() {
    // The exact index subpath avoids the package-level NativeWind redirection.
    return require("react-native/index.js").AssetRegistry;
  },
};
module.exports = Object.defineProperties({}, descriptors);
