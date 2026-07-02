# Shared helper library. Go to Symbol in Workspace ("greet", "mkBanner") finds
# these from anywhere in the monorepo; references on a name shows its uses.
{
  greet = name: "hello, ${name}";

  mkBanner = title: ''
    ==== ${title} ====
  '';
}
