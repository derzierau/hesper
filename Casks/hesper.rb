cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.3.4"
  sha256 arm: "37e5e9149c4923bedae4785c3e9332bd37a49e6cd0bd5b7164f7569b102e9511",
         intel: "a4786a9604baa1d67d7d74200b2fc40f2ff19c3dc7718aeceb240d629664cec7"

  url "https://github.com/derzierau/hesper/releases/download/v#{version}/Hesper-v#{version}-#{arch}.zip"
  name "Hesper"
  desc "Run and steer coding agents across your Macs"
  homepage "https://github.com/derzierau/hesper"
  depends_on macos: :sonoma
  container type: :zip

  app "Hesper.app"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesperctl"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesperd"
  binary "#{appdir}/Hesper.app/Contents/MacOS/hesper-keys"

  uninstall launchctl: "de.olezierau.hesperd"
  caveats <<~EOS
    Set up the daemon, agent hooks and skill after installing or upgrading:
      /bin/sh "#{appdir}/Hesper.app/Contents/Resources/hesper-setup.sh"
    In Codex, enable the hooks once with /hooks.
  EOS
end
