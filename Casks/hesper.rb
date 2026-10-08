cask "hesper" do
  arch arm: "arm64", intel: "x86_64"
  version "0.1.4"
  sha256 arm: "2cc10ff3d1b080b8023d0b7aeeffdb48277c6a641d68551533a8bfe2b999b39d",
         intel: "88d94dc137ed405c9312d3a8b51d6000864bd88d0962bfccd1ebdedf3fea1806"

  on_arm do
    url "https://api.github.com/repos/derzierau/hesper/releases/assets/622410211",
        header: ["Accept: application/octet-stream", "Authorization: Bearer #{ENV['HOMEBREW_GITHUB_API_TOKEN']}"]
  end
  on_intel do
    url "https://api.github.com/repos/derzierau/hesper/releases/assets/622410212",
        header: ["Accept: application/octet-stream", "Authorization: Bearer #{ENV['HOMEBREW_GITHUB_API_TOKEN']}"]
  end
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
