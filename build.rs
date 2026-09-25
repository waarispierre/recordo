fn main() {
    // screencapturekit compiles a Swift shim and asks the linker for the Swift runtime
    // under XcodeDefault.xctoolchain, which only exists with full Xcode installed. On a
    // Command Line Tools-only machine the same libraries live here instead.
    if cfg!(target_os = "macos") {
        for path in [
            "/Library/Developer/CommandLineTools/usr/lib/swift/macosx",
            "/usr/lib/swift",
        ] {
            if std::path::Path::new(path).is_dir() {
                println!("cargo:rustc-link-search=native={path}");
            }
        }
    }
}
