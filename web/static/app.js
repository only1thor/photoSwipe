(function() {
  'use strict';

  const SWIPE_DIST = 80;       // px threshold
  const SWIPE_VELOCITY = 0.4;  // px/ms threshold for "flick"
  const HOLD_MS = 350;         // long-press to zoom

  function currentCard() {
    return document.querySelector('#photo-area .card[data-photo-id]');
  }

  function currentPhotoId() {
    const c = currentCard();
    return c ? c.dataset.photoId : null;
  }

  function isClusterCard() {
    return !!document.querySelector('#photo-area .card[data-card-kind="cluster"]');
  }

  function decide(action) {
    // Cluster cards have their own form submit; per-photo gestures are
    // disabled. The user resolves the cluster via Apply / Skip cluster.
    if (isClusterCard()) return;
    const id = currentPhotoId();
    if (!id) return;
    if (typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/decision', {
      target: '#photo-area',
      swap: 'innerHTML',
      values: { action: action, photo_id: id }
    });
  }

  function undo() {
    if (typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/undo', {
      target: '#photo-area',
      swap: 'innerHTML'
    });
  }

  // --- keyboard ---
  document.addEventListener('keydown', function(e) {
    if (e.target.matches('input, textarea, select')) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    switch (e.key) {
      case 'ArrowLeft':
      case 'j':
      case 'J':
        e.preventDefault();
        decide('trash');
        break;
      case 'ArrowRight':
      case 'l':
      case 'L':
      case 'k':
      case 'K':
        e.preventDefault();
        decide('keep');
        break;
      case 'ArrowUp':
      case 'i':
      case 'I':
        e.preventDefault();
        decide('skip');
        break;
      case 'z':
      case 'Z':
        e.preventDefault();
        undo();
        break;
      case ' ':
        toggleZoom();
        e.preventDefault();
        break;
    }
  });

  // --- buttons (event delegation) ---
  document.addEventListener('click', function(e) {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'undo') undo();
    else decide(action);
  });

  // --- zoom (tap-and-hold / spacebar / click image) ---
  function toggleZoom() {
    const c = currentCard();
    if (!c) return;
    c.classList.toggle('zoomed');
  }
  let holdTimer = null;
  document.addEventListener('pointerdown', function(e) {
    const card = e.target.closest('.card');
    if (!card) return;
    holdTimer = setTimeout(function() {
      card.classList.toggle('zoomed');
      holdTimer = null;
    }, HOLD_MS);
  });
  document.addEventListener('pointerup', cancelHold);
  document.addEventListener('pointercancel', cancelHold);
  document.addEventListener('pointerleave', cancelHold);
  function cancelHold() {
    if (holdTimer) { clearTimeout(holdTimer); holdTimer = null; }
  }

  // --- touch/mouse drag for swipe ---
  let drag = null;
  function startDrag(e) {
    const card = e.target.closest('.card');
    if (!card || card.classList.contains('zoomed')) return;
    if (card.dataset.cardKind === 'cluster') return;
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

    let action = null;
    if (absX > absY) {
      if (dx > SWIPE_DIST || (dx > 30 && vel > SWIPE_VELOCITY)) action = 'keep';
      else if (dx < -SWIPE_DIST || (dx < -30 && vel > SWIPE_VELOCITY)) action = 'trash';
    } else {
      if (dy < -SWIPE_DIST || (dy < -30 && vel > SWIPE_VELOCITY)) action = 'skip';
    }

    const c = drag.card;
    if (action) {
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

  // Mouse drag fallback on desktop (only on the photo card area)
  document.addEventListener('mousedown', function(e) {
    if (e.button !== 0) return;
    if (!e.target.closest('#photo-area')) return;
    startDrag(e);
  });
  document.addEventListener('mousemove', function(e) {
    if (drag) moveDrag(e);
  });
  document.addEventListener('mouseup', function(e) {
    if (drag) endDrag(e);
  });
})();
