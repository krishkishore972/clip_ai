// Supplied as NativeWind's parent resolver, so its internal components redirect
// is covered too. Web and the wrapper's own dependency resolve normally.
function withAssetRegistry(compatPath, parent) {
  return (context, moduleName, platform) => {
    if (
      (platform === "android" || platform === "ios") &&
      moduleName === "react-native-css/components" &&
      context.originModulePath !== compatPath
    ) {
      return { type: "sourceFile", filePath: compatPath };
    }
    return (parent ?? context.resolveRequest)(context, moduleName, platform);
  };
}
module.exports = { withAssetRegistry };
