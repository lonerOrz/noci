{
  lib,
  buildGoModule,
  version ? "0.1.0",
}:

buildGoModule {
  pname = "noci";
  inherit version;
  src = ../.;

  vendorHash = "sha256-qdQ+GvPB+AZ9Heb28HFlHcZYpHJ4/flgE6LMVvyHGJ8=";

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X noci/cmd.version=${version}"
  ];
  doCheck = false;

  meta = {
    description = "Highly modular Nix binary cache over OCI registry";
    homepage = "https://github.com/lonerOrz/noci";
    license = lib.licenses.bsd3;
    maintainers = with lib.maintainers; [ lonerOrz ];
    mainProgram = "noci";
  };
}
