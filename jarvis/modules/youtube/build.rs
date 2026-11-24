fn main() {
    if std::env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("musl") {
        // Rust's self-contained musl combines libm into libc.a.
        let out = std::path::PathBuf::from(std::env::var_os("OUT_DIR").unwrap());
        std::fs::write(out.join("libm.a"), b"INPUT(-lc)\n").unwrap();
        println!("cargo:rustc-link-search=native={}", out.display());
    }
}
