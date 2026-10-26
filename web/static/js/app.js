// テーマの初期化と切り替え
(function () {
  const savedTheme = localStorage.getItem('md-theme') || 'light';
  document.documentElement.setAttribute('data-theme', savedTheme);
})();

function toggleTheme() {
  const current = document.documentElement.getAttribute('data-theme') || 'light';
  const next = current === 'light' ? 'dark' : (current === 'dark' ? 'emerald' : 'light');
  document.documentElement.setAttribute('data-theme', next);
  localStorage.setItem('md-theme', next);
}

function closeModal() {
  const modal = document.getElementById('modal-container');
  if (modal) {
    modal.innerHTML = '';
  }
}
