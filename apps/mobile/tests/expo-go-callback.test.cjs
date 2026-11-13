const assert = require('node:assert/strict');
const { test } = require('node:test');
const { join } = require('node:path');
const { expoGoCallback, expoGoCallbackFromHost } = require(join(process.env.UPLOAD_TEST_BUILD_DIR, 'auth/expo-go-callback.js'));

test('Expo Go LAN callback returns to root without route or auth data', () => {
  assert.equal(
    expoGoCallback('exp://192.168.1.20:8081/--/hosted-auth-callback?state=private#token'),
    'exp://192.168.1.20:8081/--/',
  );
});

test('Expo Go callback preserves secure tunnels and project paths', () => {
  assert.equal(expoGoCallback('exps://example.exp.direct'), 'exps://example.exp.direct/--/');
  assert.equal(expoGoCallback('exp://exp.host/@creator/project/--/settings?secret=value'),
    'exp://exp.host/@creator/project/--/');
});

test('Expo Go callback rejects absent, custom-scheme, HTTP and hostless URLs', () => {
  for (const input of [null, '', 'invalid', 'mobile:///hosted-auth-callback',
    'https://example.com', 'exp:///hosted-auth-callback']) {
    assert.equal(expoGoCallback(input), null);
  }
});

test('Expo Go callback is stable and strips URL credentials', () => {
  const callback = expoGoCallback('exp://user:password@192.168.1.20:8081/?secret=value');
  assert.equal(callback, 'exp://192.168.1.20:8081/--/');
  assert.equal(expoGoCallback(callback), callback);
});

test('native Expo Go development host supplies callback without launch URLs', () => {
  assert.equal(expoGoCallbackFromHost('192.168.1.20:8081'), 'exp://192.168.1.20:8081/--/');
  assert.equal(expoGoCallbackFromHost('http://localhost:8081'), 'exp://localhost:8081/--/');
  assert.equal(expoGoCallbackFromHost('https://example.exp.direct'), 'exps://example.exp.direct/--/');
  assert.equal(expoGoCallbackFromHost('[::1]:8081'), 'exp://[::1]:8081/--/');
});

test('native development host rejects absent and malformed config without leaking data', () => {
  for (const host of [undefined, '', 'not a host', 'mobile:///callback',
    'http://user:password@localhost:8081', 'localhost:8081/path',
    'localhost:8081?token=private', 'localhost:8081#private']) {
    assert.equal(expoGoCallbackFromHost(host), null);
  }
});
