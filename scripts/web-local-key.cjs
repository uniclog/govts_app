const { createHash, createPrivateKey, createPublicKey, randomBytes, X509Certificate } = require("node:crypto");
const fs = require("node:fs");

const path = process.argv[2];
if (process.argv[3] === "--tls-fingerprint") {
  const cert = new X509Certificate(fs.readFileSync(path));
  const spki = cert.publicKey.export({ format: "der", type: "spki" });
  process.stdout.write(createHash("sha256").update(spki).digest("hex").toUpperCase().match(/../g).join(":") + "\n");
  process.exit(0);
}
if (process.argv[3] === "--create") {
  try {
    fs.writeFileSync(path, randomBytes(32).toString("base64") + "\n", { flag: "wx", mode: 0o600 });
  } catch (error) {
    if (error.code !== "EEXIST") throw error;
  }
}

const seed = Buffer.from(fs.readFileSync(path, "utf8").trim(), "base64");
if (seed.length !== 32) throw new Error("Expected a 32-byte Ed25519 server seed.");

// PKCS#8 wrapper for a raw Ed25519 seed. SPKI contains the public key as its final bit string.
const prefix = Buffer.from("302e020100300506032b657004220420", "hex");
const privateKey = createPrivateKey({ key: Buffer.concat([prefix, seed]), format: "der", type: "pkcs8" });
const publicKey = createPublicKey(privateKey).export({ format: "der", type: "spki" });
process.stdout.write(publicKey.subarray(-32).toString("base64") + "\n");
