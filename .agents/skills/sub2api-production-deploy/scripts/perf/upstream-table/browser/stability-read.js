async (page) => {
  if(!page.__stability) throw new Error('stability run missing');
  return page.__stability;
}
