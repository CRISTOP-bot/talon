# Terms of Use

This file mirrors the Terms embedded in the binary. The authoritative text is
`internal/legal/assets/terms/v1.1.0.md`, which is what `talon terms` prints and
what acceptance is recorded against. A test fails if the two drift apart.

---

# Talon Terms of Use — version 1.1.0

**Effective date:** 2026-10-03
**Status:** current
**Applies to:** Talon CLI, version 0.1.x and later
**Material change:** yes — see section 10

Changes from 1.0.0:

- Section 3 states explicitly that Talon stores no account and uploads nothing.
- Section 5 adds the obligation to keep version control and backups.
- Section 7 states that a material change requires explicit acceptance and that
  Talon never accepts one for you.

## 1. What Talon is

Talon is a command-line program that runs on **your** computer. It connects to a
large language model provider you choose, gives that model a set of tools, and
performs actions on your files and shell on your behalf, subject to the
permissions you configure.

Talon is **open source software** provided under the MIT licence in `LICENSE`.

## 2. What you agree to

By running Talon you agree that:

- You are legally able to accept these terms in your jurisdiction.
- You are responsible for **what the model does on your machine**, because you
  control the permissions you grant it.
- You will review changes before they are applied, and you will not grant
  unrestricted access to repositories or systems you do not understand.

## 3. Accounts, servers and telemetry

Talon has **no accounts, no backend and no telemetry**. The Talon project
operates no service that receives your data. The model provider you configure is
a separate service with its own terms and privacy policy; prompts and any file
content Talon sends are processed under that provider's policy, not under these
terms. Talon shows you which host every request goes to before it is sent.

## 4. Acceptable use

You agree not to use Talon to:

- break the law, or infringe someone else's rights;
- access systems you are not authorised to access;
- generate or deploy malware, or bypass security controls of systems you do not
  own;
- violate the terms of the provider whose model you call.

## 5. Your responsibility for changes

Talon edits files and runs commands. You are responsible for reviewing what it
does. Keep your work in version control, read the diffs it prints, and keep
backups. Talon provides `talon permissions`, an undo journal and `read-only` mode
so you can constrain it; it cannot verify that your repository is correct.

## 6. No warranty

Talon is provided "as is", without warranty of any kind, including the implied
warranties of merchantability, fitness for a particular purpose and
non-infringement.

## 7. Limitation of liability

To the maximum extent permitted by law, the authors and contributors are not
liable for any indirect, incidental, special or consequential damages, including
lost data, lost profits or security incidents, arising from your use of Talon or
of any model you call through it.

## 8. Changes to these terms

Material changes bump the version number and appear in `CHANGELOG.md`. Talon
records which version you accepted in a local file and **never accepts a new
version on your behalf**: it tells you, shows you what changed and asks you to
accept. Declining keeps the previous terms in force for your installation.

## 9. Termination

You can stop using Talon at any time. Uninstalling it and running
`talon data clear` removes the data it stored on your machine.

## 10. Privacy and security

Data handling is described in `PRIVACY.md`. Security controls, the threat model
and known limitations are in `SECURITY.md` and `docs/security-model.md`.
