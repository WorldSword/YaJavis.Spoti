//! Local enrollment on the same feature extractor used by the speaker.
use jarvis_dsp::oww::Oww;

const RATE: usize = 16_000;
const HOP: usize = 1280;
const EMB: usize = 96;
const CONTEXT: usize = RATE * 2;
pub const SAMPLES: usize = RATE * 16 / 5;
pub const POSITIVES: usize = 10;
pub const NEGATIVES: usize = 3;
pub const BODY_BYTES: usize = (POSITIVES + NEGATIVES) * SAMPLES * 2;

struct Speech {
    start: usize,
    end: usize,
}

fn speech(pcm: &[i16]) -> Option<Speech> {
    const BLOCK: usize = 320;
    let rms: Vec<_> = pcm
        .chunks_exact(BLOCK)
        .map(|frame| {
            (frame.iter().map(|&v| (f32::from(v)).powi(2)).sum::<f32>() / BLOCK as f32).sqrt()
        })
        .collect();
    let mut sorted = rms.clone();
    sorted.sort_by(f32::total_cmp);
    let peak = sorted[sorted.len() * 95 / 100];
    let threshold = (sorted[sorted.len() / 5] * 2.4).max(80.0).max(peak * 0.07);
    let mut active = rms.windows(3).enumerate().filter_map(|(i, frame)| {
        (frame.iter().filter(|&&v| v >= threshold).count() >= 2).then_some(i + 1)
    });
    let first = active.next()?;
    let last = active.last().unwrap_or(first);
    let start = first.saturating_sub(1) * BLOCK;
    let end = (last + 2) * BLOCK;
    if last < first + 6 || start < BLOCK || end + BLOCK * 2 > pcm.len() {
        return None;
    }
    Some(Speech { start, end })
}

struct Windows {
    values: Vec<(usize, Vec<f32>)>,
}

impl Windows {
    fn of(extractor: &mut Oww, pcm: &[i16], seq: usize, gain: f32) -> Self {
        let mut signal = vec![0; CONTEXT];
        signal.extend(pcm.iter().map(|&sample| (f32::from(sample) * gain) as i16));
        signal.resize(signal.len() + 4 * HOP, 0);
        let values = extractor
            .embeddings(&signal)
            .windows(seq)
            .filter_map(|window| {
                let sample = window.last()?.0.checked_sub(CONTEXT)?;
                let row = window
                    .iter()
                    .flat_map(|(_, embedding)| embedding.iter().copied())
                    .collect();
                Some((sample, row))
            })
            .collect();
        Self { values }
    }

    fn positives(&self, speech: &Speech) -> impl Iterator<Item = &[f32]> {
        self.values
            .iter()
            .filter(move |(sample, _)| {
                *sample + HOP / 2 >= speech.end && *sample <= speech.end + 2 * HOP
            })
            .map(|(_, window)| window.as_slice())
    }

    fn before_speech(&self, speech: &Speech) -> impl Iterator<Item = &[f32]> {
        self.values
            .iter()
            .filter(move |(sample, _)| *sample < speech.start)
            .step_by(2)
            .map(|(_, window)| window.as_slice())
    }

    fn negatives(&self) -> impl Iterator<Item = &[f32]> {
        self.values.iter().map(|(_, window)| window.as_slice())
    }
}

/// Two ReLU units implement the linear discriminant using the regular OWWH format.
/// The always-on detector only keeps a short feature history and two weight vectors.
fn head(seq: usize, weights: &[f32], bias: f32, thresholds: [f32; 10]) -> Vec<u8> {
    let mut bytes = b"OWWH".to_vec();
    for value in [seq as u32, EMB as u32, 2] {
        bytes.extend(value.to_le_bytes());
    }
    for value in thresholds
        .into_iter()
        .chain(weights.iter().copied())
        .chain(weights.iter().map(|w| -*w))
        .chain([bias, -bias, 1.0, 0.0, 0.0, 1.0, 0.0, 0.0, 1.0, -1.0, 0.0])
    {
        bytes.extend(value.to_le_bytes());
    }
    bytes
}

fn sigmoid(logit: f32) -> f32 {
    1.0 / (1.0 + (-logit.clamp(-50.0, 50.0)).exp())
}

fn dot(a: &[f32], b: &[f32]) -> f32 {
    a.iter().zip(b).map(|(&a, &b)| a * b).sum()
}

fn fit(positives: &[&[f32]], negatives: &[&[f32]], length: usize) -> (Vec<f32>, f32) {
    let classes = [positives, negatives];
    let mut mean = vec![0.0; length];
    let mut spread = vec![0.0; length];
    for group in classes {
        let factor = 0.5 / group.len() as f32;
        for row in group {
            for (m, &v) in mean.iter_mut().zip(*row) {
                *m += factor * v;
            }
        }
    }
    for group in classes {
        let factor = 0.5 / group.len() as f32;
        for row in group {
            for ((s, &m), &v) in spread.iter_mut().zip(&mean).zip(*row) {
                *s += factor * (v - m).powi(2);
            }
        }
    }
    // Class balancing keeps the many negative windows from drowning out the utterances.
    let rows: Vec<_> = classes
        .into_iter()
        .enumerate()
        .flat_map(|(class, group)| {
            let mean = &mean;
            let spread = &spread;
            group.iter().map(move |row| {
                let normalized: Vec<_> = row
                    .iter()
                    .zip(mean)
                    .zip(spread)
                    .map(|((&v, &m), &s)| (v - m) / (s + 0.06).sqrt())
                    .collect();
                (
                    normalized,
                    (class == 0) as u8 as f32,
                    0.5 / group.len() as f32,
                )
            })
        })
        .collect();
    let mut weights = vec![0.0; length];
    let mut gradient = vec![0.0; length];
    let mut bias = 0.0;
    // Normalize the step by typical squared feature-vector length.
    let norm = rows
        .iter()
        .map(|(row, _, factor)| factor * dot(row, row))
        .sum::<f32>();
    let step = 3.0 / norm.max(1.0);
    for _ in 0..160 {
        for (d, w) in gradient.iter_mut().zip(&weights) {
            *d = 0.4 * w;
        }
        let mut offset = 0.0;
        for (row, target, factor) in &rows {
            let error = (sigmoid(dot(&weights, row) + bias) - target) * factor;
            offset += error;
            for (d, v) in gradient.iter_mut().zip(row) {
                *d += error * v;
            }
        }
        for (w, d) in weights.iter_mut().zip(&gradient) {
            *w -= step * d;
        }
        bias -= offset * 0.25;
    }
    for (w, variance) in weights.iter_mut().zip(spread) {
        *w /= (variance + 0.06).sqrt();
    }
    bias -= dot(&weights, &mean);
    (weights, bias)
}

pub fn train(features: &[u8], bytes: &[u8]) -> Result<Vec<u8>, String> {
    if bytes.len() != BODY_BYTES {
        return Err(format!(
            "Нужно {POSITIVES} записей фразы и {NEGATIVES} записи обычной речи или фона"
        ));
    }
    let clips: Vec<Vec<i16>> = bytes
        .chunks_exact(SAMPLES * 2)
        .map(|bytes| {
            bytes
                .chunks_exact(2)
                .map(|s| i16::from_le_bytes([s[0], s[1]]))
                .collect()
        })
        .collect();
    let speech: Vec<_> = clips[..POSITIVES].iter().enumerate().map(|(i, clip)| {
        speech(clip).ok_or_else(|| format!("В записи {} не разобрать начало или конец фразы. Произнесите её один раз, с короткой паузой до и после", i + 1))
    }).collect::<Result<_, _>>()?;
    let seq = speech
        .iter()
        .map(|s| (s.end - s.start).div_ceil(HOP) + 1)
        .max()
        .unwrap_or(16)
        .max(16);
    let length = seq * EMB;
    let blank = head(seq, &vec![0.0; length], 0.0, [0.5; 10]);
    let mut extractor = Oww::load(features, &blank, Box::new(|| 5))?;
    let windows: Vec<_> = clips
        .iter()
        .map(|pcm| Windows::of(&mut extractor, pcm, seq, 1.0))
        .collect();
    // The last two utterances and last background clip are reserved for validation.
    let split = POSITIVES - 2;
    let augmented: Vec<_> = clips[..split]
        .iter()
        .enumerate()
        .map(|(i, clip)| {
            Windows::of(
                &mut extractor,
                clip,
                seq,
                if i % 2 == 0 { 0.65 } else { 1.3 },
            )
        })
        .collect();
    let p: Vec<_> = windows[..split]
        .iter()
        .chain(&augmented)
        .zip(speech[..split].iter().cycle())
        .flat_map(|(windows, timing)| windows.positives(timing))
        .collect();
    let n: Vec<_> = windows[..split]
        .iter()
        .zip(&speech)
        .flat_map(|(w, s)| w.before_speech(s))
        .chain(
            windows[POSITIVES..windows.len() - 1]
                .iter()
                .flat_map(|w| w.negatives().step_by(2)),
        )
        .collect();
    if p.len() < split || n.is_empty() {
        return Err("Мало чистых записей для обучения".into());
    }
    let (weights, bias) = fit(&p, &n, length);
    let score = |row: &[f32]| dot(&weights, row) + bias;
    let low = windows[split..POSITIVES]
        .iter()
        .zip(&speech[split..])
        .map(|(w, s)| w.positives(s).map(score).fold(f32::NEG_INFINITY, f32::max))
        .fold(f32::INFINITY, f32::min);
    let high = windows
        .last()
        .unwrap()
        .negatives()
        .chain(n.iter().copied())
        .map(score)
        .fold(f32::NEG_INFINITY, f32::max);
    if !low.is_finite() || !high.is_finite() || low - high < 0.08 {
        return Err("Фраза сливается с фоном на проверочных записях. Запишите её чётче или попробуйте более различимую фразу".into());
    }
    let thresholds =
        std::array::from_fn(|i| sigmoid(high + (low - high) * (0.80 - i as f32 * 0.065)));
    Ok(head(seq, &weights, bias, thresholds))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_incomplete_and_silent_enrollment() {
        assert!(train(&[], &[0; 6]).is_err());
        assert!(
            train(&[], &vec![0; BODY_BYTES])
                .unwrap_err()
                .contains("записи 1")
        );
    }
}
