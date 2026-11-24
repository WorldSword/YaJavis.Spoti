fn main() {
    if std::env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("musl") {
        // Rust's self-contained musl sysroot combines libm into libc.a and does
        // not ship a separate libm.a. libopus_sys emits -lm; GNU linkers accept
        // this script as the standard musl alias, using the same target libc.
        let out = std::path::PathBuf::from(std::env::var_os("OUT_DIR").unwrap());
        std::fs::write(out.join("libm.a"), b"INPUT(-lc)\n").unwrap();
        println!("cargo:rustc-link-search=native={}", out.display());
    }
}
