{
  description = "photoSwipe — self-hosted photo sorting webapp";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              gotools
              go-tools
              git
              curl
              jq
            ];

            shellHook = ''
              export GOPATH="$PWD/.go"
              export GOCACHE="$PWD/.go/cache"
              export PATH="$GOPATH/bin:$PATH"
              echo "photoSwipe dev shell — go $(go version | awk '{print $3}')"
            '';
          };
        });
    };
}
