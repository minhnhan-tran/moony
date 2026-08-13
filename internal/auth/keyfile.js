// keyfile.js — keyfile credentials for the moony relay login and account pages.
//
// A keyfile is an ECDSA P-256 private key that never leaves the browser
// unencrypted: it is generated here, wrapped with a key derived from a passphrase
// (PBKDF2-SHA256, AES-GCM) and handed to the user as a small JSON file. Only the
// public half is sent to the relay. Logging in means signing a server-issued
// challenge, so the file's contents are never transmitted.
//
// Consequence, stated plainly in the UI: lose the file or forget its passphrase and
// that credential is gone. There is no server-side reset, because the server has
// nothing to reset.
(function () {
  "use strict";

  var KDF_ITERS = 300000; // PBKDF2-SHA256; ~0.2-0.5s in a current browser
  var FILE_VERSION = 1;
  var subtle = window.crypto && window.crypto.subtle;

  function b64(buf) {
    var bytes = new Uint8Array(buf), s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s);
  }
  function unb64(str) {
    var s = atob(str), out = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out;
  }
  function rand(n) { return window.crypto.getRandomValues(new Uint8Array(n)); }

  // wrapKey derives an AES-GCM key from the passphrase and encrypts the PKCS#8
  // private key under it. The salt and IV are fresh per file.
  function deriveKey(passphrase, salt, iters) {
    return subtle.importKey("raw", new TextEncoder().encode(passphrase), "PBKDF2", false, ["deriveKey"])
      .then(function (base) {
        return subtle.deriveKey(
          { name: "PBKDF2", salt: salt, iterations: iters, hash: "SHA-256" },
          base, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
      });
  }

  // aad binds the wrapping to the file's metadata, so editing the stored KDF
  // parameters (e.g. dropping the iteration count to 1) makes decryption fail
  // instead of silently weakening the file.
  function aad(doc) {
    return new TextEncoder().encode([
      "moony-keyfile", doc.v, doc.account || "", doc.key_id || "",
      doc.kdf.name, doc.kdf.hash, doc.kdf.iterations, doc.kdf.salt,
      doc.cipher.name, doc.cipher.iv,
    ].join("\u0000"));
  }

  function wipe(el) {
    if (el) el.value = "";
  }

  function jsonFetch(url, opts) {
    opts = opts || {};
    opts.credentials = "same-origin";
    opts.headers = opts.headers || {};
    if (opts.body) opts.headers["Content-Type"] = "application/json";
    return fetch(url, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (body) {
        if (!res.ok) throw new Error(body.error || ("request failed (" + res.status + ")"));
        return body;
      });
    });
  }

  function csrfToken() {
    var el = document.querySelector('input[name="csrf_token"]');
    return el ? el.value : "";
  }

  function setStatus(el, msg, isError) {
    if (!el) return;
    el.textContent = msg || "";
    el.className = msg ? (isError ? "auth-err" : "auth-ok") : "";
  }

  // ---- enrolment (account page) -------------------------------------------

  function download(name, text) {
    var url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
    var a = document.createElement("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Revoke late: some browsers cancel an in-flight download otherwise.
    setTimeout(function () { URL.revokeObjectURL(url); }, 30000);
  }

  function enrol(form, status) {
    var label = (form.querySelector("#key_label") || {}).value || "";
    var pass = (form.querySelector("#key_pass") || {}).value || "";
    var confirm = (form.querySelector("#key_pass2") || {}).value || "";
    if (pass.length < 10) throw new Error("Use a passphrase of at least 10 characters.");
    if (pass !== confirm) throw new Error("The two passphrases do not match.");

    var priv, keyfile;
    return subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])
      .then(function (pair) {
        priv = pair.privateKey;
        return subtle.exportKey("spki", pair.publicKey);
      })
      .then(function (spki) {
        // Enrol the public half FIRST. If this fails there is no orphan keyfile
        // sitting in the user's downloads that the relay has never heard of.
        return jsonFetch("/auth/keys", {
          method: "POST",
          headers: { "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({ key: b64(spki), label: label }),
        });
      })
      .then(function (res) {
        keyfile = { id: (res.key || {}).id || "", label: label };
        return subtle.exportKey("pkcs8", priv);
      })
      .then(function (pkcs8) {
        var salt = rand(16), iv = rand(12);
        var doc = {
          v: FILE_VERSION,
          type: "moony-keyfile",
          account: (document.body.dataset.account || ""),
          key_id: keyfile.id,
          label: keyfile.label,
          kdf: { name: "PBKDF2", hash: "SHA-256", iterations: KDF_ITERS, salt: b64(salt) },
          cipher: { name: "AES-GCM", iv: b64(iv) },
        };
        return deriveKey(pass, salt, KDF_ITERS)
          .then(function (aes) {
            return subtle.encrypt({ name: "AES-GCM", iv: iv, additionalData: aad(doc) }, aes, pkcs8);
          })
          .then(function (ct) {
            doc.key = b64(ct);
            var safe = (doc.account || "moony").replace(/[^a-zA-Z0-9._-]+/g, "_");
            download("moony-keyfile-" + safe + ".json", JSON.stringify(doc, null, 2));
            wipe(form.querySelector("#key_pass"));
            wipe(form.querySelector("#key_pass2"));
            setStatus(status, "Keyfile downloaded. Keep it safe — it cannot be recovered or reset. Reload this page to see it listed.", false);
            return doc;
          })
          .catch(function (err) {
            // The public key is already enrolled; without a keyfile it is a dead
            // entry that would still count against the per-account limit, so take
            // it back out rather than leaving the user stuck.
            return jsonFetch("/auth/keys/revoke", {
              method: "POST",
              headers: { "X-CSRF-Token": csrfToken() },
              body: JSON.stringify({ id: keyfile.id }),
            }).catch(function () { /* report the original failure below */ })
              .then(function () {
                throw new Error((err && err.message ? err.message : "Could not create the keyfile") +
                  " — nothing was enrolled.");
              });
          });
      });
  }

  // ---- login --------------------------------------------------------------

  function readFile(input) {
    return new Promise(function (resolve, reject) {
      var f = input && input.files && input.files[0];
      if (!f) return reject(new Error("Choose your keyfile first."));
      if (f.size > 64 * 1024) return reject(new Error("That does not look like a keyfile."));
      var fr = new FileReader();
      fr.onerror = function () { reject(new Error("Could not read that file.")); };
      fr.onload = function () { resolve(String(fr.result)); };
      fr.readAsText(f);
    });
  }

  function unlock(doc, pass) {
    if (!doc || doc.type !== "moony-keyfile" || doc.v !== FILE_VERSION) {
      return Promise.reject(new Error("That is not a moony keyfile."));
    }
    var kdf = doc.kdf || {}, cipher = doc.cipher || {};
    // Clamp both ends: a tampered file must not be able to make us spend minutes in
    // PBKDF2 either. The value is authenticated by the AAD below, so a mismatch
    // fails rather than weakening anything.
    var iters = Math.min(Math.max(parseInt(kdf.iterations, 10) || 0, 100000), 1000000);
    return deriveKey(pass, unb64(kdf.salt || ""), iters)
      .then(function (aes) {
        return subtle.decrypt(
          { name: "AES-GCM", iv: unb64(cipher.iv || ""), additionalData: aad(doc) },
          aes, unb64(doc.key || ""));
      })
      .catch(function () {
        // AES-GCM failure is indistinguishable from a wrong passphrase, which is
        // exactly what we tell the user.
        throw new Error("Wrong passphrase, or the keyfile is damaged.");
      })
      .then(function (pkcs8) {
        return subtle.importKey("pkcs8", pkcs8, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
      });
  }

  function keyfileLogin(panel, status) {
    var fileInput = panel.querySelector("#keyfile_file");
    var pass = (panel.querySelector("#keyfile_pass") || {}).value || "";
    var username = (document.querySelector("#username") || {}).value || "";
    var returnTo = (document.querySelector('input[name="return"]') || {}).value || "/";
    if (!username.trim()) throw new Error("Enter your username first.");

    var doc;
    return readFile(fileInput)
      .then(function (text) {
        try { doc = JSON.parse(text); } catch (e) { throw new Error("That file is not valid JSON."); }
        return unlock(doc, pass);
      })
      .then(function (priv) {
        return jsonFetch("/auth/challenge?username=" + encodeURIComponent(username))
          .then(function (res) {
            var challenge = res.challenge || "";
            return subtle.sign({ name: "ECDSA", hash: "SHA-256" }, priv,
              new TextEncoder().encode(challenge))
              .then(function (sig) {
                // WebCrypto emits raw r||s, which is what the server verifies.
                return jsonFetch("/auth/login-keyfile", {
                  method: "POST",
                  body: JSON.stringify({ challenge: challenge, signature: b64(sig), "return": returnTo }),
                });
              });
          });
      })
      .then(function (res) {
        wipe(panel.querySelector("#keyfile_pass"));
        setStatus(status, "Signed in. Redirecting…", false);
        // The server already restricts this to a same-host path; re-check here so a
        // future server change cannot turn the response into an open redirect.
        var to = res["return"] || "/";
        if (to.charAt(0) !== "/" || to.charAt(1) === "/" || to.charAt(1) === "\\") to = "/";
        window.location.assign(to);
      });
  }

  // ---- revocation ---------------------------------------------------------

  function revoke(button, status) {
    var id = button.getAttribute("data-key-id") || "";
    if (!id) return Promise.reject(new Error("Missing key id."));
    if (!window.confirm("Revoke this keyfile? Anyone holding it will no longer be able to sign in.")) {
      return Promise.resolve();
    }
    return jsonFetch("/auth/keys/revoke", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ id: id }),
    }).then(function () {
      var row = button.closest("li");
      if (row) row.remove();
      setStatus(status, "Key revoked.", false);
    });
  }

  // ---- wiring -------------------------------------------------------------

  function busy(btn, on, label) {
    if (!btn) return;
    btn.disabled = on;
    if (on) {
      btn.dataset.idle = btn.textContent;
      btn.textContent = label || "Working…";
    } else if (btn.dataset.idle) {
      btn.textContent = btn.dataset.idle;
    }
  }

  function run(btn, status, fn, label) {
    setStatus(status, "", false);
    busy(btn, true, label);
    Promise.resolve()
      .then(fn)
      .catch(function (err) { setStatus(status, err && err.message ? err.message : String(err), true); })
      .then(function () { busy(btn, false); });
  }

  document.addEventListener("DOMContentLoaded", function () {
    var unsupported = !subtle || !window.crypto.getRandomValues;
    document.querySelectorAll("[data-keyfile-unsupported]").forEach(function (el) {
      el.hidden = !unsupported;
    });
    document.querySelectorAll("[data-keyfile-panel]").forEach(function (el) {
      if (unsupported) el.hidden = true;
    });
    if (unsupported) return;

    var enrolForm = document.querySelector("#keyfile_enrol");
    if (enrolForm) {
      var enrolStatus = document.querySelector("#keyfile_enrol_status");
      enrolForm.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var btn = enrolForm.querySelector("button[type=submit]");
        run(btn, enrolStatus, function () { return enrol(enrolForm, enrolStatus); }, "Generating…");
      });
    }

    var loginPanel = document.querySelector("#keyfile_login");
    if (loginPanel) {
      var loginStatus = document.querySelector("#keyfile_login_status");
      loginPanel.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var btn = loginPanel.querySelector("button[type=submit]");
        run(btn, loginStatus, function () { return keyfileLogin(loginPanel, loginStatus); }, "Signing in…");
      });
    }

    document.querySelectorAll("[data-key-id]").forEach(function (btn) {
      btn.addEventListener("click", function (ev) {
        ev.preventDefault();
        run(btn, document.querySelector("#keyfile_keys_status"), function () {
          return revoke(btn, document.querySelector("#keyfile_keys_status"));
        }, "Revoking…");
      });
    });
  });
})();
