(function() {
  'use strict';

  const SWIPE_DIST = 80;       // px threshold
  const SWIPE_VELOCITY = 0.4;  // px/ms threshold for "flick"
  const TAP_SLOP = 10;         // px movement still counts as a tap

  function currentCard() {
    return document.querySelector('#photo-area .card[data-photo-id]');
  }
  function currentClusterCard() {
    return document.querySelector('#photo-area .card[data-card-kind="cluster"]');
  }
  function clusterIsExpanded(card) {
    return card && card.classList.contains('expanded');
  }

  function decide(action) {
    const cluster = currentClusterCard();
    if (cluster) { handleClusterAction(action, cluster); return; }
    const id = currentCard()?.dataset.photoId;
    if (!id || typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/decision', {
      target: '#photo-area', swap: 'innerHTML',
      values: { action: action, photo_id: id }
    });
  }

  function undo() {
    if (typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/undo', { target: '#photo-area', swap: 'innerHTML' });
  }

  function handleClusterAction(action, card) {
    const form = card.querySelector('form');
    if (!form) return;
    if (!clusterIsExpanded(card)) {
      if (action === 'keep') { card.classList.add('expanded'); return; }
      if (action === 'trash') { form.requestSubmit(); return; }
      if (action === 'skip') {
        const btn = form.querySelector('button[name="action"][value="skip"]');
        if (btn) btn.click();
        return;
      }
    }
    // Expanded: per-photo buttons are inert; user uses Apply / Skip.
  }

  // Zoom is "tap to enter, tap to exit". For single-photo cards we toggle
  // .zoomed on the card; for cluster thumbs we toggle it on the .cluster-thumb.
  function zoomElement(el) {
    if (!el) return;
    el.classList.toggle('zoomed');
  }

  // --- keyboard ---
  document.addEventListener('keydown', function(e) {
    if (e.target.matches('input, textarea, select')) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    switch (e.key) {
      case 'ArrowLeft': case 'j': case 'J':
        e.preventDefault(); decide('trash'); break;
      case 'ArrowRight': case 'l': case 'L': case 'k': case 'K':
        e.preventDefault(); decide('keep'); break;
      case 'ArrowUp': case 'i': case 'I':
        e.preventDefault(); decide('skip'); break;
      case 'z': case 'Z':
        e.preventDefault(); undo(); break;
      case ' ':
        e.preventDefault(); zoomElement(currentCard()); break;
      case 'Escape':
        document.querySelectorAll('.zoomed').forEach(el => el.classList.remove('zoomed'));
        break;
    }
  });

  // --- buttons + info-toggle ---
  document.addEventListener('click', function(e) {
    const infoBtn = e.target.closest('[data-info-toggle]');
    if (infoBtn) {
      const card = infoBtn.closest('.card');
      if (card) {
        const show = !card.classList.contains('show-info');
        card.classList.toggle('show-info', show);
        if (show) {
          const panel = card.querySelector('.card-meta.panel');
          if (panel && typeof htmx !== 'undefined') htmx.trigger(panel, 'info-show');
        }
      }
      return;
    }
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'undo') undo(); else decide(action);
  });

  // --- click-to-zoom on cluster thumbs (separate from the corner check) ---
  document.addEventListener('click', function(e) {
    // First: tap on a zoomed cluster thumb exits zoom.
    const zoomed = e.target.closest('.cluster-thumb.zoomed');
    if (zoomed) {
      // If the user actually tapped the corner check inside zoom, let the
      // checkbox toggle normally and don't exit.
      if (!e.target.closest('.check-corner')) {
        zoomed.classList.remove('zoomed');
        e.preventDefault();
      }
      return;
    }
    // Then: tap on a non-zoomed cluster thumb (anywhere except the corner)
    // enters zoom.
    const thumb = e.target.closest('.cluster-thumb');
    if (thumb && !e.target.closest('.check-corner')) {
      thumb.classList.add('zoomed');
      e.preventDefault();
    }
  });

  // --- touch/mouse drag for swipe; tap-with-no-movement = zoom toggle on single cards ---
  let drag = null;
  function startDrag(e) {
    // Never start a drag from form controls, buttons, labels, or the info icon.
    if (e.target.closest('input, button, label, [data-info-toggle], .check-corner')) return;
    const card = e.target.closest('.card');
    if (!card) return;
    // No swipes inside an expanded cluster grid; the grid scrolls / taps.
    if (card.dataset.cardKind === 'cluster' && card.classList.contains('expanded')) return;
    // No swipes on a zoomed card either (let the user pan/pinch).
    if (card.classList.contains('zoomed')) return;
    const point = e.touches ? e.touches[0] : e;
    drag = {
      card: card,
      x0: point.clientX, y0: point.clientY,
      t0: performance.now()
    };
  }
  function moveDrag(e) {
    if (!drag) return;
    const point = e.touches ? e.touches[0] : e;
    const dx = point.clientX - drag.x0;
    const dy = point.clientY - drag.y0;
    if (Math.abs(dx) < 4 && Math.abs(dy) < 4) return; // ignore noise
    drag.card.style.transform = `translate(${dx}px, ${dy}px) rotate(${dx * 0.04}deg)`;
    drag.card.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
    const absX = Math.abs(dx), absY = Math.abs(dy);
    if (absX > absY) {
      if (dx > 30) drag.card.classList.add('swipe-keep');
      else if (dx < -30) drag.card.classList.add('swipe-trash');
    } else if (dy < -30) {
      drag.card.classList.add('swipe-skip');
    }
  }
  function endDrag(e) {
    if (!drag) return;
    const point = e.changedTouches ? e.changedTouches[0] : e;
    const dx = point.clientX - drag.x0;
    const dy = point.clientY - drag.y0;
    const dt = performance.now() - drag.t0;
    const absX = Math.abs(dx), absY = Math.abs(dy);
    const vel = Math.max(absX, absY) / Math.max(dt, 1);
    const c = drag.card;
    const isCluster = c.dataset.cardKind === 'cluster';

    let action = null;
    if (absX > absY) {
      if (dx > SWIPE_DIST || (dx > 30 && vel > SWIPE_VELOCITY)) action = 'keep';
      else if (dx < -SWIPE_DIST || (dx < -30 && vel > SWIPE_VELOCITY)) action = 'trash';
    } else {
      if (dy < -SWIPE_DIST || (dy < -30 && vel > SWIPE_VELOCITY)) action = 'skip';
    }

    // Pure tap (no swipe + barely moved) → toggle zoom. Cluster collapsed
    // stack: tap expands the grid instead.
    if (!action && absX < TAP_SLOP && absY < TAP_SLOP) {
      c.style.transition = '';
      c.style.transform = '';
      c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
      if (isCluster && !c.classList.contains('expanded')) {
        c.classList.add('expanded');
      } else if (!isCluster) {
        c.classList.toggle('zoomed');
      }
      drag = null;
      return;
    }

    if (action) {
      if (isCluster && action === 'keep') {
        c.style.transition = 'transform 0.15s ease-out';
        c.style.transform = '';
        c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
        c.classList.add('expanded');
        drag = null;
        return;
      }
      c.style.transition = 'transform 0.18s ease-out, opacity 0.18s ease-out';
      const tx = action === 'keep' ? '120vw' : action === 'trash' ? '-120vw' : '0';
      const ty = action === 'skip' ? '-100vh' : '0';
      c.style.transform = `translate(${tx}, ${ty})`;
      c.style.opacity = 0;
      decide(action);
    } else {
      c.style.transition = 'transform 0.18s ease-out';
      c.style.transform = '';
      c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
    }
    drag = null;
  }

  document.addEventListener('touchstart', startDrag, { passive: true });
  document.addEventListener('touchmove', moveDrag, { passive: true });
  document.addEventListener('touchend', endDrag, { passive: true });
  document.addEventListener('mousedown', function(e) {
    if (e.button !== 0) return;
    if (!e.target.closest('#photo-area')) return;
    startDrag(e);
  });
  document.addEventListener('mousemove', function(e) { if (drag) moveDrag(e); });
  document.addEventListener('mouseup', function(e) { if (drag) endDrag(e); });
})();
