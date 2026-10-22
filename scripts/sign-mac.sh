#!/usr/bin/env bash
# Assina e notariza o Gantry.app (Developer ID da Apple) e confere.
#   scripts/sign-mac.sh build/bin/Gantry.app
#
# Precisa destes segredos no GitHub (Settings › Secrets › Actions):
#   APPLE_CERT_P12       o certificado "Developer ID Application" exportado
#                        do Keychain (.p12), em base64 (base64 -i cert.p12)
#   APPLE_CERT_PASSWORD  a senha do .p12
#   APPLE_ID             o e-mail da conta Apple Developer
#   APPLE_APP_PASSWORD   senha de app (appleid.apple.com › Senhas de app)
#   APPLE_TEAM_ID        o Team ID (developer.apple.com › Membership)
#
# Sem eles, sai sem assinatura (com aviso) e o resto da release segue igual.
# Com eles, qualquer falha (assinatura, notarização, conferência) para o job,
# e a release não é publicada.
set -euo pipefail
APP="$1"

if [ -z "${APPLE_CERT_P12:-}" ] || [ -z "${APPLE_ID:-}" ] || [ -z "${APPLE_TEAM_ID:-}" ] || [ -z "${APPLE_APP_PASSWORD:-}" ]; then
  echo "::warning::Sem os segredos da Apple (APPLE_CERT_P12, APPLE_ID, APPLE_TEAM_ID, APPLE_APP_PASSWORD): o app sai sem assinatura."
  echo "assinado=nao" >>"${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

# Um keychain só deste build, com o certificado.
KC="$RUNNER_TEMP/gantry.keychain-db"
KCPASS=$(openssl rand -hex 16)
security create-keychain -p "$KCPASS" "$KC"
security set-keychain-settings -lut 3600 "$KC"
security unlock-keychain -p "$KCPASS" "$KC"
echo "$APPLE_CERT_P12" | base64 --decode >"$RUNNER_TEMP/cert.p12"
security import "$RUNNER_TEMP/cert.p12" -k "$KC" -P "${APPLE_CERT_PASSWORD:-}" -T /usr/bin/codesign
rm -f "$RUNNER_TEMP/cert.p12"
# shellcheck disable=SC2046 # the keychains already listed, one per word
security list-keychains -d user -s "$KC" $(security list-keychains -d user | tr -d '"')
security set-key-partition-list -S apple-tool:,apple: -s -k "$KCPASS" "$KC" >/dev/null
ID=$(security find-identity -v -p codesigning "$KC" | awk -F'"' '/Developer ID Application/ { print $2; exit }')
[ -n "$ID" ] || { echo "::error::O certificado não é um Developer ID Application."; exit 1; }
echo "Assinando com: $ID"

# Hardened runtime (exigido pela notarização), com carimbo de hora; os
# executáveis de dentro primeiro, o app por último.
find "$APP/Contents" -type f -perm -u+x ! -path "*/MacOS/*" -print0 | while IFS= read -r -d '' f; do
  codesign --force --options runtime --timestamp --keychain "$KC" -s "$ID" "$f"
done
codesign --force --options runtime --timestamp --keychain "$KC" -s "$ID" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"

# Notarização: o zip vai para a Apple; o carimbo volta grampeado no app.
ZIP="$RUNNER_TEMP/notarizar.zip"
ditto -c -k --keepParent "$APP" "$ZIP"
xcrun notarytool submit "$ZIP" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait --timeout 30m | tee "$RUNNER_TEMP/notary.txt"
grep -q "status: Accepted" "$RUNNER_TEMP/notary.txt" || {
  echo "::error::A Apple recusou a notarização; veja o log com xcrun notarytool log <id>."
  exit 1
}
xcrun stapler staple "$APP"
xcrun stapler validate "$APP"

# O que o Mac de quem baixa vai conferir.
spctl -a -vv -t exec "$APP" 2>&1 | tee "$RUNNER_TEMP/spctl.txt"
grep -q "source=Notarized Developer ID" "$RUNNER_TEMP/spctl.txt" || { echo "::error::O Gatekeeper não aceitou o app."; exit 1; }
security delete-keychain "$KC" || true
echo "assinado=sim" >>"${GITHUB_OUTPUT:-/dev/null}"
