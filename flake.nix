{
  description = "";

  inputs = {
    nixpkgs.url = "nixpkgs";
  };

  outputs =
    { self, nixpkgs }:
    let
      # Systems supported
      allSystems = [
        "x86_64-linux" # 64-bit Intel/AMD Linux
      ];

      # Helper to provide system-specific attributes
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs allSystems (
          system:
          f {
            pkgs = import nixpkgs { inherit system; };
          }
        );
    in
    {
      devShells = forAllSystems (
        { pkgs }:
        {
          default = pkgs.mkShell {
            # The Nix packages provided in the environment
            hardeningDisable = [ "fortify" ];
            packages = with pkgs; [
              go_1_24
              protobuf
              protoc-gen-go
              libopus
              gotools
              golangci-lint
              delve
              gcc
              pkg-config

              # Audio (portaudio + opus)
              portaudio
              libopus

              # GUI (Fyne)
              libGL
              xorg.libX11
              xorg.libXcursor
              xorg.libXrandr
              xorg.libXinerama
              xorg.libXi
              xorg.libXxf86vm
            ];

          };
        }
      );
    };
}
