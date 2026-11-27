'use strict';

const { createPrivateKey } = require('node:crypto');

// postman-runtime only uses Forge to convert RSA private keys for ASAP auth.
// KeyObject handles replace its ASN.1 objects; parsing and export use OpenSSL.
function rsaKey(key) {
  if (key.type !== 'private' || key.asymmetricKeyType !== 'rsa') {
    throw new TypeError('ASAP data URI must contain an RSA private key');
  }
  return key;
}

function fromDer(bytes) {
  const key = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes, 'binary');
  try {
    return rsaKey(createPrivateKey({ key, format: 'der', type: 'pkcs8' }));
  } catch {
    return rsaKey(createPrivateKey({ key, format: 'der', type: 'pkcs1' }));
  }
}

module.exports = {
  util: { decode64: (value) => Buffer.from(value, 'base64').toString('binary') },
  asn1: { fromDer },
  pki: {
    privateKeyFromAsn1: rsaKey,
    privateKeyFromPem: (pem) => rsaKey(createPrivateKey(pem)),
    privateKeyToPem: (key) => rsaKey(key).export({ format: 'pem', type: 'pkcs1' }),
    privateKeyToAsn1: rsaKey,
    wrapRsaPrivateKey: rsaKey,
    privateKeyInfoToPem: (key) => rsaKey(key).export({ format: 'pem', type: 'pkcs8' }),
  },
};
