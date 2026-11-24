//! Звук Джарвиса: спектр и детекторы слова «Джарвис». Порт Go-версии один в один —
//! совпадение проверяется тестами на тех же моделях и векторах (Python, «золотые» данные Go).

pub mod audio;
pub mod kws;
pub mod oww;

/// Детектор слова активации: звук 16 кГц моно кусками любой длины.
pub trait Wake: Send {
    /// true — только что прозвучало «Джарвис».
    fn feed(&mut self, pcm: &[i16]) -> bool;
    /// После разговора: не слушать хвост своей же речи.
    fn reset(&mut self);
    /// Последняя вероятность (для панели и журнала).
    fn last_prob(&self) -> f32;
    /// Вероятность в момент срабатывания.
    fn wake_prob(&self) -> f32 {
        self.last_prob()
    }
}

/// Классический радикс-2 FFT на месте (как в Go: те же операции в том же порядке).
pub fn fft(re: &mut [f64], im: &mut [f64]) {
    let n = re.len();
    let mut j = 0usize;
    for i in 1..n {
        let mut bit = n >> 1;
        while j & bit != 0 {
            j ^= bit;
            bit >>= 1;
        }
        j ^= bit;
        if i < j {
            re.swap(i, j);
            im.swap(i, j);
        }
    }
    let mut size = 2;
    while size <= n {
        let ang = -2.0 * std::f64::consts::PI / size as f64;
        let (wr, wi) = (ang.cos(), ang.sin());
        let mut start = 0;
        while start < n {
            let (mut cr, mut ci) = (1.0f64, 0.0f64);
            for k in 0..size / 2 {
                let (a, b) = (start + k, start + k + size / 2);
                let tr = re[b] * cr - im[b] * ci;
                let ti = re[b] * ci + im[b] * cr;
                re[b] = re[a] - tr;
                im[b] = im[a] - ti;
                re[a] += tr;
                im[a] += ti;
                let ncr = cr * wr - ci * wi;
                ci = cr * wi + ci * wr;
                cr = ncr;
            }
            start += size;
        }
        size <<= 1;
    }
}

/// Скалярное произведение и матричные ядра (с ARM64 NEON и портируемым фоллбэком).
#[cfg(target_arch = "aarch64")]
mod simd {
    use std::arch::aarch64::*;

    #[inline]
    pub fn dot(a: &[f32], b: &[f32]) -> f32 {
        let n = a.len();
        assert!(b.len() >= n);
        let pa = a.as_ptr();
        let pb = b.as_ptr();
        unsafe {
            let mut s0 = vdupq_n_f32(0.0);
            let mut s1 = vdupq_n_f32(0.0);
            let mut s2 = vdupq_n_f32(0.0);
            let mut s3 = vdupq_n_f32(0.0);
            let mut i = 0usize;
            while i + 16 <= n {
                s0 = vfmaq_f32(s0, vld1q_f32(pa.add(i)), vld1q_f32(pb.add(i)));
                s1 = vfmaq_f32(s1, vld1q_f32(pa.add(i + 4)), vld1q_f32(pb.add(i + 4)));
                s2 = vfmaq_f32(s2, vld1q_f32(pa.add(i + 8)), vld1q_f32(pb.add(i + 8)));
                s3 = vfmaq_f32(s3, vld1q_f32(pa.add(i + 12)), vld1q_f32(pb.add(i + 12)));
                i += 16;
            }
            s0 = vaddq_f32(vaddq_f32(s0, s1), vaddq_f32(s2, s3));
            while i + 4 <= n {
                s0 = vfmaq_f32(s0, vld1q_f32(pa.add(i)), vld1q_f32(pb.add(i)));
                i += 4;
            }
            let mut sum = vaddvq_f32(s0);
            while i < n {
                sum += *pa.add(i) * *pb.add(i);
                i += 1;
            }
            sum
        }
    }

    #[inline]
    pub fn gemv(dst: &mut [f32], src: &[f32], w: &[f32], stride: usize) {
        let n = dst.len();
        let k = src.len();
        if n == 0 || k == 0 {
            return;
        }
        assert!(stride >= n);
        assert!(w.len() >= (k - 1) * stride + n);
        let pd = dst.as_mut_ptr();
        let ps = src.as_ptr();
        let pw = w.as_ptr();
        unsafe {
            let mut c = 0usize;
            while c + 16 <= n {
                let mut d0 = vld1q_f32(pd.add(c));
                let mut d1 = vld1q_f32(pd.add(c + 4));
                let mut d2 = vld1q_f32(pd.add(c + 8));
                let mut d3 = vld1q_f32(pd.add(c + 12));
                for i in 0..k {
                    let sv = vdupq_n_f32(*ps.add(i));
                    let row = pw.add(i * stride + c);
                    d0 = vfmaq_f32(d0, sv, vld1q_f32(row));
                    d1 = vfmaq_f32(d1, sv, vld1q_f32(row.add(4)));
                    d2 = vfmaq_f32(d2, sv, vld1q_f32(row.add(8)));
                    d3 = vfmaq_f32(d3, sv, vld1q_f32(row.add(12)));
                }
                vst1q_f32(pd.add(c), d0);
                vst1q_f32(pd.add(c + 4), d1);
                vst1q_f32(pd.add(c + 8), d2);
                vst1q_f32(pd.add(c + 12), d3);
                c += 16;
            }
            while c + 8 <= n {
                let mut d0 = vld1q_f32(pd.add(c));
                let mut d1 = vld1q_f32(pd.add(c + 4));
                for i in 0..k {
                    let sv = vdupq_n_f32(*ps.add(i));
                    let row = pw.add(i * stride + c);
                    d0 = vfmaq_f32(d0, sv, vld1q_f32(row));
                    d1 = vfmaq_f32(d1, sv, vld1q_f32(row.add(4)));
                }
                vst1q_f32(pd.add(c), d0);
                vst1q_f32(pd.add(c + 4), d1);
                c += 8;
            }
            while c + 4 <= n {
                let mut d0 = vld1q_f32(pd.add(c));
                for i in 0..k {
                    let sv = vdupq_n_f32(*ps.add(i));
                    d0 = vfmaq_f32(d0, sv, vld1q_f32(pw.add(i * stride + c)));
                }
                vst1q_f32(pd.add(c), d0);
                c += 4;
            }
            while c < n {
                let mut acc = *pd.add(c);
                for i in 0..k {
                    acc += *ps.add(i) * *pw.add(i * stride + c);
                }
                *pd.add(c) = acc;
                c += 1;
            }
        }
    }

    #[inline]
    pub fn gemm4(
        dst: &mut [f32],
        dst_stride: usize,
        src: &[f32],
        src_stride: usize,
        w: &[f32],
        k: usize,
        w_stride: usize,
        n: usize,
    ) {
        if n == 0 || k == 0 {
            return;
        }
        assert!(dst_stride >= n && w_stride >= n);
        assert!(dst.len() >= 3 * dst_stride + n);
        assert!(src.len() >= 3 * src_stride + k);
        assert!(w.len() >= (k - 1) * w_stride + n);
        unsafe {
            let pd0 = dst.as_mut_ptr();
            let pd1 = pd0.add(dst_stride);
            let pd2 = pd1.add(dst_stride);
            let pd3 = pd2.add(dst_stride);
            let ps0 = src.as_ptr();
            let ps1 = ps0.add(src_stride);
            let ps2 = ps1.add(src_stride);
            let ps3 = ps2.add(src_stride);
            let pw = w.as_ptr();
            let mut c = 0usize;
            while c + 16 <= n {
                let (mut a00, mut a01, mut a02, mut a03) = (
                    vld1q_f32(pd0.add(c)),
                    vld1q_f32(pd0.add(c + 4)),
                    vld1q_f32(pd0.add(c + 8)),
                    vld1q_f32(pd0.add(c + 12)),
                );
                let (mut a10, mut a11, mut a12, mut a13) = (
                    vld1q_f32(pd1.add(c)),
                    vld1q_f32(pd1.add(c + 4)),
                    vld1q_f32(pd1.add(c + 8)),
                    vld1q_f32(pd1.add(c + 12)),
                );
                let (mut a20, mut a21, mut a22, mut a23) = (
                    vld1q_f32(pd2.add(c)),
                    vld1q_f32(pd2.add(c + 4)),
                    vld1q_f32(pd2.add(c + 8)),
                    vld1q_f32(pd2.add(c + 12)),
                );
                let (mut a30, mut a31, mut a32, mut a33) = (
                    vld1q_f32(pd3.add(c)),
                    vld1q_f32(pd3.add(c + 4)),
                    vld1q_f32(pd3.add(c + 8)),
                    vld1q_f32(pd3.add(c + 12)),
                );
                for i in 0..k {
                    let row = pw.add(i * w_stride + c);
                    let w0 = vld1q_f32(row);
                    let w1 = vld1q_f32(row.add(4));
                    let w2 = vld1q_f32(row.add(8));
                    let w3 = vld1q_f32(row.add(12));
                    let s0 = vdupq_n_f32(*ps0.add(i));
                    let s1 = vdupq_n_f32(*ps1.add(i));
                    let s2 = vdupq_n_f32(*ps2.add(i));
                    let s3 = vdupq_n_f32(*ps3.add(i));
                    a00 = vfmaq_f32(a00, s0, w0);
                    a01 = vfmaq_f32(a01, s0, w1);
                    a02 = vfmaq_f32(a02, s0, w2);
                    a03 = vfmaq_f32(a03, s0, w3);
                    a10 = vfmaq_f32(a10, s1, w0);
                    a11 = vfmaq_f32(a11, s1, w1);
                    a12 = vfmaq_f32(a12, s1, w2);
                    a13 = vfmaq_f32(a13, s1, w3);
                    a20 = vfmaq_f32(a20, s2, w0);
                    a21 = vfmaq_f32(a21, s2, w1);
                    a22 = vfmaq_f32(a22, s2, w2);
                    a23 = vfmaq_f32(a23, s2, w3);
                    a30 = vfmaq_f32(a30, s3, w0);
                    a31 = vfmaq_f32(a31, s3, w1);
                    a32 = vfmaq_f32(a32, s3, w2);
                    a33 = vfmaq_f32(a33, s3, w3);
                }
                vst1q_f32(pd0.add(c), a00);
                vst1q_f32(pd0.add(c + 4), a01);
                vst1q_f32(pd0.add(c + 8), a02);
                vst1q_f32(pd0.add(c + 12), a03);
                vst1q_f32(pd1.add(c), a10);
                vst1q_f32(pd1.add(c + 4), a11);
                vst1q_f32(pd1.add(c + 8), a12);
                vst1q_f32(pd1.add(c + 12), a13);
                vst1q_f32(pd2.add(c), a20);
                vst1q_f32(pd2.add(c + 4), a21);
                vst1q_f32(pd2.add(c + 8), a22);
                vst1q_f32(pd2.add(c + 12), a23);
                vst1q_f32(pd3.add(c), a30);
                vst1q_f32(pd3.add(c + 4), a31);
                vst1q_f32(pd3.add(c + 8), a32);
                vst1q_f32(pd3.add(c + 12), a33);
                c += 16;
            }
            while c + 8 <= n {
                let (mut a00, mut a01) = (vld1q_f32(pd0.add(c)), vld1q_f32(pd0.add(c + 4)));
                let (mut a10, mut a11) = (vld1q_f32(pd1.add(c)), vld1q_f32(pd1.add(c + 4)));
                let (mut a20, mut a21) = (vld1q_f32(pd2.add(c)), vld1q_f32(pd2.add(c + 4)));
                let (mut a30, mut a31) = (vld1q_f32(pd3.add(c)), vld1q_f32(pd3.add(c + 4)));
                for i in 0..k {
                    let row = pw.add(i * w_stride + c);
                    let w0 = vld1q_f32(row);
                    let w1 = vld1q_f32(row.add(4));
                    let s0 = vdupq_n_f32(*ps0.add(i));
                    let s1 = vdupq_n_f32(*ps1.add(i));
                    let s2 = vdupq_n_f32(*ps2.add(i));
                    let s3 = vdupq_n_f32(*ps3.add(i));
                    a00 = vfmaq_f32(a00, s0, w0);
                    a01 = vfmaq_f32(a01, s0, w1);
                    a10 = vfmaq_f32(a10, s1, w0);
                    a11 = vfmaq_f32(a11, s1, w1);
                    a20 = vfmaq_f32(a20, s2, w0);
                    a21 = vfmaq_f32(a21, s2, w1);
                    a30 = vfmaq_f32(a30, s3, w0);
                    a31 = vfmaq_f32(a31, s3, w1);
                }
                vst1q_f32(pd0.add(c), a00);
                vst1q_f32(pd0.add(c + 4), a01);
                vst1q_f32(pd1.add(c), a10);
                vst1q_f32(pd1.add(c + 4), a11);
                vst1q_f32(pd2.add(c), a20);
                vst1q_f32(pd2.add(c + 4), a21);
                vst1q_f32(pd3.add(c), a30);
                vst1q_f32(pd3.add(c + 4), a31);
                c += 8;
            }
            while c + 4 <= n {
                let mut a0 = vld1q_f32(pd0.add(c));
                let mut a1 = vld1q_f32(pd1.add(c));
                let mut a2 = vld1q_f32(pd2.add(c));
                let mut a3 = vld1q_f32(pd3.add(c));
                for i in 0..k {
                    let w0 = vld1q_f32(pw.add(i * w_stride + c));
                    a0 = vfmaq_f32(a0, vdupq_n_f32(*ps0.add(i)), w0);
                    a1 = vfmaq_f32(a1, vdupq_n_f32(*ps1.add(i)), w0);
                    a2 = vfmaq_f32(a2, vdupq_n_f32(*ps2.add(i)), w0);
                    a3 = vfmaq_f32(a3, vdupq_n_f32(*ps3.add(i)), w0);
                }
                vst1q_f32(pd0.add(c), a0);
                vst1q_f32(pd1.add(c), a1);
                vst1q_f32(pd2.add(c), a2);
                vst1q_f32(pd3.add(c), a3);
                c += 4;
            }
            while c < n {
                let (mut a0, mut a1, mut a2, mut a3) =
                    (*pd0.add(c), *pd1.add(c), *pd2.add(c), *pd3.add(c));
                for i in 0..k {
                    let wv = *pw.add(i * w_stride + c);
                    a0 += *ps0.add(i) * wv;
                    a1 += *ps1.add(i) * wv;
                    a2 += *ps2.add(i) * wv;
                    a3 += *ps3.add(i) * wv;
                }
                *pd0.add(c) = a0;
                *pd1.add(c) = a1;
                *pd2.add(c) = a2;
                *pd3.add(c) = a3;
                c += 1;
            }
        }
    }

    #[inline]
    pub fn gemm2(
        dst: &mut [f32],
        dst_stride: usize,
        src: &[f32],
        src_stride: usize,
        w: &[f32],
        k: usize,
        w_stride: usize,
        n: usize,
    ) {
        if n == 0 || k == 0 {
            return;
        }
        assert!(dst_stride >= n && w_stride >= n);
        assert!(dst.len() >= dst_stride + n);
        assert!(src.len() >= src_stride + k);
        assert!(w.len() >= (k - 1) * w_stride + n);
        unsafe {
            let pd0 = dst.as_mut_ptr();
            let pd1 = pd0.add(dst_stride);
            let ps0 = src.as_ptr();
            let ps1 = ps0.add(src_stride);
            let pw = w.as_ptr();
            let mut c = 0usize;
            while c + 16 <= n {
                let (mut a00, mut a01, mut a02, mut a03) = (
                    vld1q_f32(pd0.add(c)),
                    vld1q_f32(pd0.add(c + 4)),
                    vld1q_f32(pd0.add(c + 8)),
                    vld1q_f32(pd0.add(c + 12)),
                );
                let (mut a10, mut a11, mut a12, mut a13) = (
                    vld1q_f32(pd1.add(c)),
                    vld1q_f32(pd1.add(c + 4)),
                    vld1q_f32(pd1.add(c + 8)),
                    vld1q_f32(pd1.add(c + 12)),
                );
                for i in 0..k {
                    let row = pw.add(i * w_stride + c);
                    let w0 = vld1q_f32(row);
                    let w1 = vld1q_f32(row.add(4));
                    let w2 = vld1q_f32(row.add(8));
                    let w3 = vld1q_f32(row.add(12));
                    let s0 = vdupq_n_f32(*ps0.add(i));
                    let s1 = vdupq_n_f32(*ps1.add(i));
                    a00 = vfmaq_f32(a00, s0, w0);
                    a01 = vfmaq_f32(a01, s0, w1);
                    a02 = vfmaq_f32(a02, s0, w2);
                    a03 = vfmaq_f32(a03, s0, w3);
                    a10 = vfmaq_f32(a10, s1, w0);
                    a11 = vfmaq_f32(a11, s1, w1);
                    a12 = vfmaq_f32(a12, s1, w2);
                    a13 = vfmaq_f32(a13, s1, w3);
                }
                vst1q_f32(pd0.add(c), a00);
                vst1q_f32(pd0.add(c + 4), a01);
                vst1q_f32(pd0.add(c + 8), a02);
                vst1q_f32(pd0.add(c + 12), a03);
                vst1q_f32(pd1.add(c), a10);
                vst1q_f32(pd1.add(c + 4), a11);
                vst1q_f32(pd1.add(c + 8), a12);
                vst1q_f32(pd1.add(c + 12), a13);
                c += 16;
            }
            while c + 8 <= n {
                let (mut a00, mut a01) = (vld1q_f32(pd0.add(c)), vld1q_f32(pd0.add(c + 4)));
                let (mut a10, mut a11) = (vld1q_f32(pd1.add(c)), vld1q_f32(pd1.add(c + 4)));
                for i in 0..k {
                    let row = pw.add(i * w_stride + c);
                    let w0 = vld1q_f32(row);
                    let w1 = vld1q_f32(row.add(4));
                    let s0 = vdupq_n_f32(*ps0.add(i));
                    let s1 = vdupq_n_f32(*ps1.add(i));
                    a00 = vfmaq_f32(a00, s0, w0);
                    a01 = vfmaq_f32(a01, s0, w1);
                    a10 = vfmaq_f32(a10, s1, w0);
                    a11 = vfmaq_f32(a11, s1, w1);
                }
                vst1q_f32(pd0.add(c), a00);
                vst1q_f32(pd0.add(c + 4), a01);
                vst1q_f32(pd1.add(c), a10);
                vst1q_f32(pd1.add(c + 4), a11);
                c += 8;
            }
            while c + 4 <= n {
                let mut a0 = vld1q_f32(pd0.add(c));
                let mut a1 = vld1q_f32(pd1.add(c));
                for i in 0..k {
                    let w0 = vld1q_f32(pw.add(i * w_stride + c));
                    a0 = vfmaq_f32(a0, vdupq_n_f32(*ps0.add(i)), w0);
                    a1 = vfmaq_f32(a1, vdupq_n_f32(*ps1.add(i)), w0);
                }
                vst1q_f32(pd0.add(c), a0);
                vst1q_f32(pd1.add(c), a1);
                c += 4;
            }
            while c < n {
                let (mut a0, mut a1) = (*pd0.add(c), *pd1.add(c));
                for i in 0..k {
                    let wv = *pw.add(i * w_stride + c);
                    a0 += *ps0.add(i) * wv;
                    a1 += *ps1.add(i) * wv;
                }
                *pd0.add(c) = a0;
                *pd1.add(c) = a1;
                c += 1;
            }
        }
    }
}

#[cfg(not(target_arch = "aarch64"))]
mod simd {
    #[inline]
    pub fn dot(a: &[f32], b: &[f32]) -> f32 {
        let b = &b[..a.len()];
        let (mut s0, mut s1, mut s2, mut s3) = (0f32, 0f32, 0f32, 0f32);
        let mut ca = a.chunks_exact(4);
        let mut cb = b.chunks_exact(4);
        for (x, y) in (&mut ca).zip(&mut cb) {
            s0 += x[0] * y[0];
            s1 += x[1] * y[1];
            s2 += x[2] * y[2];
            s3 += x[3] * y[3];
        }
        for (x, y) in ca.remainder().iter().zip(cb.remainder()) {
            s0 += x * y;
        }
        (s0 + s1) + (s2 + s3)
    }

    #[inline]
    pub fn gemv(dst: &mut [f32], src: &[f32], w: &[f32], stride: usize) {
        let n = dst.len();
        for (i, &v) in src.iter().enumerate() {
            let row = &w[i * stride..i * stride + n];
            for (d, &r) in dst.iter_mut().zip(row) {
                *d += v * r;
            }
        }
    }

    #[inline]
    pub fn gemm4(
        dst: &mut [f32],
        dst_stride: usize,
        src: &[f32],
        src_stride: usize,
        w: &[f32],
        k: usize,
        w_stride: usize,
        n: usize,
    ) {
        if n == 0 || k == 0 {
            return;
        }
        assert!(dst_stride >= n && w_stride >= n);
        assert!(dst.len() >= 3 * dst_stride + n);
        assert!(src.len() >= 3 * src_stride + k);
        assert!(w.len() >= (k - 1) * w_stride + n);
        let mut c = 0usize;
        while c + 4 <= n {
            let mut a = [[0f32; 4]; 4];
            for j in 0..4 {
                a[j].copy_from_slice(&dst[j * dst_stride + c..j * dst_stride + c + 4]);
            }
            for i in 0..k {
                let wr = &w[i * w_stride + c..i * w_stride + c + 4];
                for j in 0..4 {
                    let s = src[j * src_stride + i];
                    for u in 0..4 {
                        a[j][u] += s * wr[u];
                    }
                }
            }
            for j in 0..4 {
                dst[j * dst_stride + c..j * dst_stride + c + 4].copy_from_slice(&a[j]);
            }
            c += 4;
        }
        while c < n {
            let mut a0 = dst[c];
            let mut a1 = dst[dst_stride + c];
            let mut a2 = dst[2 * dst_stride + c];
            let mut a3 = dst[3 * dst_stride + c];
            for i in 0..k {
                let wv = w[i * w_stride + c];
                a0 += src[i] * wv;
                a1 += src[src_stride + i] * wv;
                a2 += src[2 * src_stride + i] * wv;
                a3 += src[3 * src_stride + i] * wv;
            }
            dst[c] = a0;
            dst[dst_stride + c] = a1;
            dst[2 * dst_stride + c] = a2;
            dst[3 * dst_stride + c] = a3;
            c += 1;
        }
    }

    #[inline]
    pub fn gemm2(
        dst: &mut [f32],
        dst_stride: usize,
        src: &[f32],
        src_stride: usize,
        w: &[f32],
        k: usize,
        w_stride: usize,
        n: usize,
    ) {
        if n == 0 || k == 0 {
            return;
        }
        assert!(dst_stride >= n && w_stride >= n);
        assert!(dst.len() >= dst_stride + n);
        assert!(src.len() >= src_stride + k);
        assert!(w.len() >= (k - 1) * w_stride + n);
        let mut c = 0usize;
        while c + 4 <= n {
            let mut a0 = [dst[c], dst[c + 1], dst[c + 2], dst[c + 3]];
            let mut a1 = [
                dst[dst_stride + c],
                dst[dst_stride + c + 1],
                dst[dst_stride + c + 2],
                dst[dst_stride + c + 3],
            ];
            for i in 0..k {
                let wr = &w[i * w_stride + c..i * w_stride + c + 4];
                let s0 = src[i];
                let s1 = src[src_stride + i];
                for u in 0..4 {
                    a0[u] += s0 * wr[u];
                    a1[u] += s1 * wr[u];
                }
            }
            dst[c..c + 4].copy_from_slice(&a0);
            dst[dst_stride + c..dst_stride + c + 4].copy_from_slice(&a1);
            c += 4;
        }
        while c < n {
            let mut a0 = dst[c];
            let mut a1 = dst[dst_stride + c];
            for i in 0..k {
                let wv = w[i * w_stride + c];
                a0 += src[i] * wv;
                a1 += src[src_stride + i] * wv;
            }
            dst[c] = a0;
            dst[dst_stride + c] = a1;
            c += 1;
        }
    }
}

/// dst[c] += Σ src[i] · w[i·stride + c]  (свёртки: выходные каналы подряд — векторизуется).
#[inline]
pub fn gemv(dst: &mut [f32], src: &[f32], w: &[f32], stride: usize) {
    simd::gemv(dst, src, w, stride);
}

/// Четыре соседних положения j=0..3: dst[j*dst_stride + c] += Σ src[j*src_stride + i] * w[i*w_stride + c].
#[inline]
pub fn gemm4(
    dst: &mut [f32],
    dst_stride: usize,
    src: &[f32],
    src_stride: usize,
    w: &[f32],
    k: usize,
    w_stride: usize,
    n: usize,
) {
    simd::gemm4(dst, dst_stride, src, src_stride, w, k, w_stride, n);
}

/// Два соседних положения j=0..1: dst[j*dst_stride + c] += Σ src[j*src_stride + i] * w[i*w_stride + c].
#[inline]
pub fn gemm2(
    dst: &mut [f32],
    dst_stride: usize,
    src: &[f32],
    src_stride: usize,
    w: &[f32],
    k: usize,
    w_stride: usize,
    n: usize,
) {
    simd::gemm2(dst, dst_stride, src, src_stride, w, k, w_stride, n);
}

/// Скалярное произведение: четыре независимые суммы (как dotGo).
#[inline]
pub fn dot(a: &[f32], b: &[f32]) -> f32 {
    simd::dot(a, b)
}

/// Предвычисленный БПФ вещественного сигнала в `f32` без тригонометрии на кадрах (NEON-дружественный).
pub(crate) struct Rfft {
    m: usize,
    bitrev: Vec<usize>,
    stage_re: Vec<Vec<f32>>,
    stage_im: Vec<Vec<f32>>,
    unp_re: Vec<f32>,
    unp_im: Vec<f32>,
    re: Vec<f32>,
    im: Vec<f32>,
}

impl Rfft {
    pub(crate) fn new(n: usize) -> Self {
        assert!(n >= 4 && n.is_power_of_two());
        let m = n / 2;
        let bits = m.trailing_zeros() as usize;
        let bitrev = (0..m).map(|i| i.reverse_bits() >> (usize::BITS as usize - bits)).collect();
        let mut stage_re = Vec::with_capacity(bits);
        let mut stage_im = Vec::with_capacity(bits);
        let mut size = 2;
        while size <= m {
            let half = size / 2;
            let mut sre = Vec::with_capacity(half);
            let mut sim = Vec::with_capacity(half);
            for k in 0..half {
                let ang = -2.0 * std::f64::consts::PI * k as f64 / size as f64;
                sre.push(ang.cos() as f32);
                sim.push(ang.sin() as f32);
            }
            stage_re.push(sre);
            stage_im.push(sim);
            size <<= 1;
        }
        let mut unp_re = Vec::with_capacity(m / 2);
        let mut unp_im = Vec::with_capacity(m / 2);
        for k in 0..m / 2 {
            let ang = -2.0 * std::f64::consts::PI * k as f64 / n as f64;
            unp_re.push(ang.cos() as f32);
            unp_im.push(ang.sin() as f32);
        }
        Rfft {
            m,
            bitrev,
            stage_re,
            stage_im,
            unp_re,
            unp_im,
            re: vec![0.0; m],
            im: vec![0.0; m],
        }
    }

    /// Квадрат модуля спектра `input[0..n]` в `out[0..=n/2]`.
    pub(crate) fn power(&mut self, input: &[f32], out: &mut [f32]) {
        let m = self.m;
        debug_assert_eq!(input.len(), m * 2);
        debug_assert_eq!(out.len(), m + 1);
        for (k, &rev) in self.bitrev.iter().enumerate() {
            self.re[rev] = input[k * 2];
            self.im[rev] = input[k * 2 + 1];
        }
        let mut half = 1;
        for (sre, sim) in self.stage_re.iter().zip(&self.stage_im) {
            let size = half * 2;
            for start in (0..m).step_by(size) {
                let (re_lo, re_hi) = self.re[start..start + size].split_at_mut(half);
                let (im_lo, im_hi) = self.im[start..start + size].split_at_mut(half);
                for k in 0..half {
                    let wr = sre[k];
                    let wi = sim[k];
                    let br = re_hi[k];
                    let bi = im_hi[k];
                    let tr = br * wr - bi * wi;
                    let ti = br * wi + bi * wr;
                    re_hi[k] = re_lo[k] - tr;
                    im_hi[k] = im_lo[k] - ti;
                    re_lo[k] += tr;
                    im_lo[k] += ti;
                }
            }
            half = size;
        }
        let r0 = self.re[0];
        let i0 = self.im[0];
        out[0] = (r0 + i0) * (r0 + i0);
        out[m] = (r0 - i0) * (r0 - i0);
        let q = m / 2;
        if m >= 2 {
            out[q] = self.re[q] * self.re[q] + self.im[q] * self.im[q];
        }
        for k in 1..q {
            let mk = m - k;
            let er = 0.5 * (self.re[k] + self.re[mk]);
            let ei = 0.5 * (self.im[k] - self.im[mk]);
            let or = 0.5 * (self.im[k] + self.im[mk]);
            let oi = -0.5 * (self.re[k] - self.re[mk]);
            let wr = self.unp_re[k];
            let wi = self.unp_im[k];
            let tr = or * wr - oi * wi;
            let ti = or * wi + oi * wr;
            let pr = er + tr;
            let pi = ei + ti;
            let nr = er - tr;
            let ni = ei - ti;
            out[k] = pr * pr + pi * pi;
            out[mk] = nr * nr + ni * ni;
        }
    }
}

/// Чтение моделей: little-endian u32 и f32 подряд.
pub(crate) struct Bin<'a> {
    b: &'a [u8],
    pub err: bool,
}

impl<'a> Bin<'a> {
    pub fn new(b: &'a [u8]) -> Self {
        Bin { b, err: false }
    }
    pub fn rest(&self) -> usize {
        self.b.len()
    }
    pub fn u32(&mut self) -> usize {
        if self.b.len() < 4 {
            self.err = true;
            return 0;
        }
        let v = u32::from_le_bytes(self.b[..4].try_into().unwrap());
        self.b = &self.b[4..];
        v as usize
    }
    pub fn f32s(&mut self, n: usize) -> Vec<f32> {
        let Some(bytes) = n.checked_mul(4) else {
            self.err = true;
            return Vec::new();
        };
        if self.err || self.b.len() < bytes {
            self.err = true;
            return vec![0.0; n];
        }
        let v = self.b[..bytes].chunks_exact(4).map(|c| f32::from_le_bytes(c.try_into().unwrap())).collect();
        self.b = &self.b[bytes..];
        v
    }
    pub fn i16s_scaled(&mut self, rows: usize, cols: usize) -> Vec<f32> {
        let scales = self.f32s(rows);
        if self.err || scales.iter().any(|s| !s.is_finite()) {
            self.err = true;
            return Vec::new();
        }
        let Some(total) = rows.checked_mul(cols) else {
            self.err = true;
            return Vec::new();
        };
        let Some(bytes) = total.checked_mul(2) else {
            self.err = true;
            return Vec::new();
        };
        if self.b.len() < bytes {
            self.err = true;
            return Vec::new();
        }
        let mut out = Vec::with_capacity(total);
        for (r, &scale) in scales.iter().enumerate() {
            let row = &self.b[r * cols * 2..(r + 1) * cols * 2];
            for c in row.chunks_exact(2) {
                let q = i16::from_le_bytes([c[0], c[1]]);
                out.push((q as f32) * scale);
            }
        }
        self.b = &self.b[bytes..];
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fft_of_impulse_is_flat() {
        let mut re = vec![0.0; 8];
        let mut im = vec![0.0; 8];
        re[0] = 1.0;
        fft(&mut re, &mut im);
        assert!(re.iter().all(|v| (v - 1.0).abs() < 1e-12) && im.iter().all(|v| v.abs() < 1e-12));
    }

    #[test]
    fn dot_gemv_and_gemm_kernels_match_scalar_reference() {
        let a: Vec<f32> = (0..10).map(|i| i as f32).collect();
        assert_eq!(dot(&a, &a), 285.0);
        let mut d = vec![1.0, 1.0];
        gemv(&mut d, &[1.0, 2.0], &[1.0, 2.0, 3.0, 4.0], 2);
        assert_eq!(d, vec![8.0, 11.0]);

        let mut seed = 1234567u32;
        let mut next = || {
            seed = seed.wrapping_mul(1664525).wrapping_add(1013904223);
            ((seed >> 16) as f32 / 32768.0) - 1.0
        };
        for &n in &[1usize, 3, 4, 7, 8, 12, 16, 24, 31, 48, 72, 96] {
            let a_v: Vec<f32> = (0..n + 5).map(|_| next()).collect();
            let b_v: Vec<f32> = (0..n + 5).map(|_| next()).collect();
            let want_dot: f32 = a_v.iter().zip(&b_v).map(|(x, y)| x * y).sum();
            assert!((dot(&a_v, &b_v) - want_dot).abs() < 1e-4, "dot len {}", a_v.len());

            let k = 17usize;
            let dst_stride = n + 3;
            let src_stride = k + 2;
            let w_stride = n + 1;
            let dst_init: Vec<f32> = (0..4 * dst_stride).map(|_| next()).collect();
            let src: Vec<f32> = (0..4 * src_stride).map(|_| next()).collect();
            let w: Vec<f32> = (0..k * w_stride).map(|_| next()).collect();

            let mut ref_dst = dst_init.clone();
            for j in 0..4 {
                for i in 0..k {
                    for c in 0..n {
                        ref_dst[j * dst_stride + c] += src[j * src_stride + i] * w[i * w_stride + c];
                    }
                }
            }

            let mut got_v = dst_init.clone();
            gemv(&mut got_v[..n], &src[..k], &w, w_stride);
            for c in 0..n {
                assert!((got_v[c] - ref_dst[c]).abs() < 1e-4, "gemv n={n} c={c}");
            }

            let mut got_2 = dst_init.clone();
            gemm2(&mut got_2, dst_stride, &src, src_stride, &w, k, w_stride, n);
            for j in 0..2 {
                for c in 0..n {
                    let idx = j * dst_stride + c;
                    assert!((got_2[idx] - ref_dst[idx]).abs() < 1e-4, "gemm2 n={n} j={j} c={c}");
                }
            }

            let mut got_4 = dst_init.clone();
            gemm4(&mut got_4, dst_stride, &src, src_stride, &w, k, w_stride, n);
            for j in 0..4 {
                for c in 0..n {
                    let idx = j * dst_stride + c;
                    assert!((got_4[idx] - ref_dst[idx]).abs() < 1e-4, "gemm4 n={n} j={j} c={c}");
                }
            }
        }
    }

    #[test]
    fn bin_i16_scaled_decodes_and_rejects_short_buffers() {
        let mut raw = Vec::new();
        raw.extend_from_slice(&0.5f32.to_le_bytes());
        raw.extend_from_slice(&(-2.0f32).to_le_bytes());
        for q in [10i16, -20, 3, 4] {
            raw.extend_from_slice(&q.to_le_bytes());
        }
        let mut bin = Bin::new(&raw);
        let got = bin.i16s_scaled(2, 2);
        assert!(!bin.err && bin.rest() == 0);
        assert_eq!(got, vec![5.0, -10.0, -6.0, -8.0]);

        let mut short = Bin::new(&raw[..raw.len() - 1]);
        assert!(short.i16s_scaled(2, 2).is_empty() && short.err);
    }

    #[test]
    fn rfft_matches_reference_fft() {
        let mut x = [0f32; 512];
        let mut re = [0f64; 512];
        let mut im = [0f64; 512];
        let mut seed = 42u32;
        for i in 0..512 {
            seed = seed.wrapping_mul(1664525).wrapping_add(1013904223);
            x[i] = ((seed >> 16) as f32 / 32768.0) - 1.0;
            re[i] = x[i] as f64;
        }
        fft(&mut re, &mut im);
        let mut rfft = Rfft::new(512);
        let mut got = [0f32; 257];
        rfft.power(&x, &mut got);
        for b in 0..257 {
            let want = (re[b] * re[b] + im[b] * im[b]) as f32;
            let err = (got[b] - want).abs();
            assert!(err <= want.abs() * 1e-4 + 1e-4, "bin {b}: got {got_b} want {want}", got_b = got[b]);
        }
    }
}
